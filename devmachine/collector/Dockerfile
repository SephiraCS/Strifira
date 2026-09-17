FROM golang:1.25.12-alpine AS build
WORKDIR /src
COPY collector/main.go .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /collector main.go

FROM alpine:3.22
RUN apk add --no-cache ca-certificates coreutils git \
    && addgroup -g 1000 ku \
    && adduser -D -u 1000 -G ku ku
COPY --from=build /collector /usr/local/bin/ku-collector
COPY common/ku-service-entrypoint /usr/local/bin/ku-service-entrypoint
RUN chmod 0755 /usr/local/bin/ku-service-entrypoint /usr/local/bin/ku-collector

USER 1000:1000
ENV HOME=/home/ku
EXPOSE 8091
CMD ["/usr/local/bin/ku-collector"]
