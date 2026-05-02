FROM golang:1.22 AS build
WORKDIR /src
COPY go.mod ./
COPY cmd ./cmd
COPY internal ./internal
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 go build -trimpath -o /out/provider ./cmd/provider

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/provider /provider
USER nonroot:nonroot
ENTRYPOINT ["/provider"]
