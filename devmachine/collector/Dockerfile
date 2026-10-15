FROM docker.io/library/golang:1.27.2-alpine AS build
WORKDIR /src
COPY collector/main.go .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /collector main.go

FROM docker.io/library/alpine:3.22
RUN apk upgrade --no-cache && apk add --no-cache ca-certificates coreutils git util-linux \
    && addgroup -g 1000 strifira \
    && adduser -D -u 1000 -G strifira strifira
COPY --from=build /collector /usr/local/bin/strifira-collector
COPY common/strifira-service-entrypoint /usr/local/bin/strifira-service-entrypoint
RUN chmod 0755 /usr/local/bin/strifira-service-entrypoint /usr/local/bin/strifira-collector

USER 1000:1000
ENV HOME=/home/strifira
EXPOSE 8091
CMD ["/usr/local/bin/strifira-collector"]
