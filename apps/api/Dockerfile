# Built from the repository root so the image can carry the migrations the
# binary embeds. Render's build context is the repo, not the service directory.

# ---- build ----
FROM golang:1.26-alpine AS build
WORKDIR /src

COPY apps/api/go.mod apps/api/go.sum ./
RUN go mod download

COPY apps/api/ ./
RUN CGO_ENABLED=0 GOOS=linux go build -ldflags="-s -w" -o /out/api       ./cmd/api
RUN CGO_ENABLED=0 GOOS=linux go build -ldflags="-s -w" -o /out/bootstrap ./cmd/bootstrap

# ---- run ----
FROM alpine:3.20
RUN apk add --no-cache ca-certificates tzdata && adduser -D -u 10001 tender
COPY --from=build /out/api       /usr/local/bin/api
COPY --from=build /out/bootstrap /usr/local/bin/bootstrap

USER tender
EXPOSE 8080
ENV PORT=8080
CMD ["/usr/local/bin/api"]
