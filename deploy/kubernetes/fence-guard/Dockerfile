FROM golang:1.22 AS build
WORKDIR /src
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/clusterguard-k8s-fence-guard ./cmd/clusterguard-k8s-fence-guard

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/clusterguard-k8s-fence-guard /clusterguard-k8s-fence-guard
ENTRYPOINT ["/clusterguard-k8s-fence-guard"]
