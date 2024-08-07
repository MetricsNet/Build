ARG ALPINE_VERSION=3.20

FROM alpine:$ALPINE_VERSION AS builder

COPY ./rathole/target /target
RUN cp /target/$(uname -m)-unknown-linux-musl/release/rathole /target/rathole

FROM alpine:$ALPINE_VERSION

COPY --from=builder /target/rathole /usr/local/bin/rathole

RUN \
  apk add --no-cache libgcc

ENTRYPOINT ["/usr/local/bin/rathole"]
