# Snippet Exercise

## Design Decision

I found building services with the layers in mind, starting from the highest level:

- Transport Layer (Http/grpc)
- Service Metrics Layer
- Service Safety (Balancing & Limiting)
- Biz Analytics
- Biz Metrics
- Biz Logic

The separation of layers promotes robust, reliable, maintainable micro services (modeled after go-kit). I tried to
implement as many as possible to give an example of what I look for in the genesis of a rest/service.

For my repo / storage layer, I chose to wrap `sync.Map` instead of implementing my own map w/ locks after learning that
it takes care of thundering heard automagically (mentioning because I am aware of a bare map w/ locks).

## Wishes

### `Ratelimiting / Backpressure / Timeouts`

- Would be a nice middleware or wrapper ideally around a client
- Timeouts are set at the server level; however it would be nice to incorporate this at the service method level as
  well.

### Dockerfile

- I can write a mean dockerfile

### Better error handling

- I would like to have an error type, and bubble it up to a mapper[errorType] which would return the proper
  error/response code.
- [Example of a PR I made](https://sourcegraph.com/github.com/sourcegraph/sourcegraph/-/commit/eab7a0595728e06831b3edde3a87521b2dc2b477?visible=6)

### Build to scale

- The repo/store would need to be centeralized at the least. At the moment, each instance has its own store.
