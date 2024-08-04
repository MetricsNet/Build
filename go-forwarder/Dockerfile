ARG ALPINE_VERSION=3.20

FROM alpine:$ALPINE_VERSION AS builder

COPY ./build /build
RUN cp /build/$(uname -m) /go-forwarder

FROM alpine:$ALPINE_VERSION

COPY --from=builder /go-forwarder /usr/local/bin/go-forwarder

ENTRYPOINT ["/usr/local/bin/go-forwarder"]
