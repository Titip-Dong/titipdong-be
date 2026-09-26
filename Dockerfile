# Build a Linux binary for the architecture requested by Docker Buildx.
FROM --platform=$BUILDPLATFORM golang:1.24-alpine AS build

ARG TARGETOS
ARG TARGETARCH

WORKDIR /src

COPY go.mod go.sum ./
RUN go mod download

COPY . ./
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -trimpath -ldflags="-s -w" -o /out/titipdong-api ./cmd/api

# Run the binary with an unprivileged user.
FROM alpine:3.21

RUN addgroup -S app && adduser -S app -G app
COPY --from=build --chown=app:app /out/titipdong-api /titipdong-api

USER app
EXPOSE 8080

ENTRYPOINT ["/titipdong-api"]
