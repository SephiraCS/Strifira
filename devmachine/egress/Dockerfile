FROM docker.io/library/golang:1.27.2-alpine AS build
WORKDIR /src
COPY egress/main.go .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /egress main.go

FROM docker.io/library/alpine:3.22
RUN apk upgrade --no-cache && apk add --no-cache ca-certificates \
    && addgroup -g 1000 strifira \
    && adduser -D -u 1000 -G strifira strifira
COPY --from=build /egress /usr/local/bin/strifira-egress
COPY common/strifira-service-entrypoint /usr/local/bin/strifira-service-entrypoint
RUN chmod 0755 /usr/local/bin/strifira-service-entrypoint /usr/local/bin/strifira-egress

USER 1000:1000
EXPOSE 3128
CMD ["/usr/local/bin/strifira-egress"]
