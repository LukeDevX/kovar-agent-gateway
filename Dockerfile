FROM golang:1.26-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -o /gateway ./cmd/gateway && CGO_ENABLED=0 go build -trimpath -o /migrate ./cmd/migrate

FROM build AS check
RUN go vet ./... && go test ./...

FROM alpine:3.23
RUN apk add --no-cache ca-certificates && adduser -D -u 10001 gateway
COPY --from=build /gateway /migrate /usr/local/bin/
COPY configs /app/configs
WORKDIR /app
USER gateway
EXPOSE 8080
HEALTHCHECK --interval=10s --timeout=3s --start-period=10s --retries=6 \
  CMD wget -q -O /dev/null http://127.0.0.1:8080/ready || exit 1
ENTRYPOINT ["gateway"]
