package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io/ioutil"
	"log"
	"math/rand"
	"net/http"
	"os"
	"os/signal"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gorilla/mux"
)

type key int

const (
	requestIDKey key = 0
)

var (
	// Version is the version of the app
	Version string = ""
	// GitTag is the git tag of the app
	GitTag string = ""
	// GitCommit is the commit hash of the app
	GitCommit string = ""
	// GitTreeState represents dirty or clean states
	GitTreeState string = ""
	listenAddr   string = ""
	healthy      int32
)

func init() {
	rand.Seed(time.Now().UnixNano())
}

func main() {
	flag.StringVar(&listenAddr, "listen-addr", ":3000", "server listen address")
	flag.Parse()

	logger := log.New(os.Stdout, "http: ", log.LstdFlags)

	logger.Println("Simple Snippet Service")
	logger.Println("Version:", Version)
	logger.Println("GitTag:", GitTag)
	logger.Println("GitCommit:", GitCommit)
	logger.Println("GitTreeState:", GitTreeState)

	logger.Println("Server is starting ...")

	service := NewSnippetService()

	router := mux.NewRouter()
	router.Handle("/health", health())
	router.Handle("/snippets", Create(service)).Methods("POST")
	router.Handle("/snippets/{name}", Get(service)).Methods("GET")
	router.Handle("/snippets/{name}/like", Like(service)).Methods("POST")

	nextRequestID := func() string {
		return fmt.Sprintf("%d", time.Now().UnixNano())
	}

	lh := logging(logger)
	th := tracing(nextRequestID)
	tracingLogger := th(lh(router))

	server := &http.Server{
		Addr:         listenAddr,
		Handler:      tracingLogger,
		ErrorLog:     logger,
		ReadTimeout:  5 * time.Second,
		WriteTimeout: 10 * time.Second,
		IdleTimeout:  15 * time.Second,
	}

	done := make(chan bool)
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, os.Interrupt)

	go func() {
		<-quit
		logger.Println("Server is shutting down...")
		atomic.StoreInt32(&healthy, 0)

		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()

		server.SetKeepAlivesEnabled(false)
		if err := server.Shutdown(ctx); err != nil {
			logger.Fatalf("Could not gracefully shutdown the server: %v\n", err)
		}
		close(done)
	}()
	logger.Println("Server is ready to handle requests at", listenAddr)
	atomic.StoreInt32(&healthy, 1)
	if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		logger.Fatalf("Could not listen on %s: %b\n", listenAddr, err)
	}
	<-done
	logger.Println("Server stopped")
}

type TTLMap struct {
	TTL time.Duration

	data sync.Map
}

type expireEntry struct {
	ExpiresAt time.Time
	Value     interface{}
}

func (t *TTLMap) Store(key string, ts time.Time, val interface{}) {
	t.data.Store(key, expireEntry{
		ExpiresAt: ts,
		Value:     val,
	})
}

func (t *TTLMap) Load(key string) (val interface{}) {
	entry, ok := t.data.Load(key)
	if !ok {
		return nil
	}

	expireEntry := entry.(expireEntry)
	if expireEntry.ExpiresAt.Before(time.Now()) {
		return nil
	}

	return expireEntry.Value
}

func NewTTLMap() (m *TTLMap) {
	go func() {
		for now := range time.Tick(time.Second) {
			m.data.Range(func(k, v interface{}) bool {
				if now.After(v.(expireEntry).ExpiresAt) {
					log.Println("removing ", k)
					m.data.Delete(k)
				}
				return true
			})
		}
	}()

	return &TTLMap{data: sync.Map{}}
}

type Snippet struct {
	URL       string    `json:"url"`
	Name      string    `json:"name"`
	ExpiresIn *int      `json:"expires_in,omitempty"`
	ExpiresAt time.Time `json:"expires_at"`
	Snippet   string    `json:"snippet"`
	Likes     *int      `json:"likes,omitempty"`
}

type (
	omit            *struct{}
	SnippetResponse struct {
		*Snippet
		ExpiresIn omit `json:"-"`
	}
)

func (s *Snippet) SetExpiresAt() {
	timein := time.Now().Add(time.Second * time.Duration(*s.ExpiresIn))
	s.ExpiresAt = timein
}

type SnippetService interface {
	Like(ctx context.Context, name string) (Snippet, error)
	Get(ctx context.Context, name string) (Snippet, error)
	Create(ctx context.Context, sn Snippet) (Snippet, error)
}

type snippetService struct {
	repo *TTLMap
	urls *TTLMap
}

func NewSnippetService() *snippetService {
	return &snippetService{
		repo: NewTTLMap(),
		urls: NewTTLMap(),
	}
}

func (s *snippetService) Like(ctx context.Context, name string) (Snippet, error) {
	snippet := s.repo.Load(name)
	if snippet == nil {
		return Snippet{}, errors.New("snippet not found")
	}
	sn := snippet.(Snippet)
	if sn.Name == "" {
		return Snippet{}, errors.New("snippet not found")
	}
	if sn.Likes == nil {
		sn.Likes = new(int)
	}
	// hacky ptr(ints) for omits
	n := *sn.Likes
	n++
	t := new(int)
	*t = 30
	//
	sn.Likes = &n
	sn.ExpiresIn = t
	sn.SetExpiresAt()
	s.repo.Store(name, sn.ExpiresAt, sn)

	return sn, nil
}

func (s *snippetService) Get(ctx context.Context, name string) (Snippet, error) {
	snippet := s.repo.Load(name)
	if snippet == nil {
		return Snippet{}, errors.New("snippet not found")
	}
	sn := snippet.(Snippet)
	if sn.Name == "" {
		return Snippet{}, errors.New("snippet not found")
	}
	go func() {
		t := new(int)
		*t = 30
		sn.ExpiresIn = t
		sn.SetExpiresAt()
		s.repo.Store(name, sn.ExpiresAt, sn)
	}()
	return sn, nil
}

func (s *snippetService) Create(ctx context.Context, snippet Snippet) (Snippet, error) {
	snippet.URL = fmt.Sprintf("%s/snippets/%s", listenAddr, snippet.Name)
	s.repo.Store(snippet.Name, snippet.ExpiresAt, snippet)
	snippet.ExpiresIn = nil
	snippet.Likes = nil
	return snippet, nil
}

func Get(srv SnippetService) http.Handler {
	return http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		params := mux.Vars(r)
		snippetKey := params["name"]

		k, err := srv.Get(ctx, snippetKey)
		if err != nil {
			http.Error(rw, err.Error(), http.StatusNotFound)
			return
		}
		k.ExpiresIn = nil
		json.NewEncoder(rw).Encode(k)
	})
}

func Create(srv SnippetService) http.Handler {
	return http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		sbytes, err := ioutil.ReadAll(r.Body)
		if err != nil {
			http.Error(rw, err.Error(), http.StatusNotFound)
			return
		}
		snippet := Snippet{}
		if err := json.Unmarshal(sbytes, &snippet); err != nil {
			http.Error(rw, err.Error(), http.StatusNotFound)
			return
		}

		snippet.SetExpiresAt()
		snippet, err = srv.Create(ctx, snippet)
		if err != nil {
			http.Error(rw, err.Error(), http.StatusNotFound)
			return
		}
		json.NewEncoder(rw).Encode(snippet)
	})
}

func Like(srv SnippetService) http.Handler {
	return http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		params := mux.Vars(r)
		snippetKey := params["name"]

		s, err := srv.Get(ctx, snippetKey)
		if err != nil {
			http.Error(rw, err.Error(), http.StatusNotFound)
			return
		}

		snippet, err := srv.Like(ctx, s.Name)
		if err != nil {
			http.Error(rw, err.Error(), http.StatusNotFound)
			return
		}
		json.NewEncoder(rw).Encode(snippet)
	})
}

// convient
func health() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if atomic.LoadInt32(&healthy) == 0 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
}

// quick logger
func logging(logger *log.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			defer func() {
				requestID, ok := r.Context().Value(requestIDKey).(string)
				if !ok {
					requestID = "unknown"
				}
				logger.Println(requestID, r.Method, r.URL.Path, r.RemoteAddr, r.UserAgent())
			}()
			next.ServeHTTP(w, r)
		})
	}
}

// quick tracer
func tracing(nextRequestID func() string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			requestID := r.Header.Get("X-Request-ID")
			if requestID == "" {
				requestID = nextRequestID()
			}
			ctx := context.WithValue(context.Background(), requestIDKey, requestID)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

type stop struct {
	error
}
