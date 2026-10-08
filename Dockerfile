FROM --platform=$BUILDPLATFORM golang:1.27.1-bookworm AS build
ARG TARGETOS=linux
ARG TARGETARCH=amd64
ARG VERSION=dev
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -trimpath -ldflags="-s -w -X main.version=$VERSION" -o /radchat ./cmd/radchat
RUN mkdir /state && chown 65532:65532 /state && chmod 700 /state

FROM scratch
COPY --from=build /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt
COPY --from=build /radchat /radchat
COPY --from=build --chown=65532:65532 /state /state
USER 65532:65532
ENV RADCHAT_DATA=/state RADCHAT_HTTP=0.0.0.0:8788 RADCHAT_P2P=/ip4/0.0.0.0/tcp/4001
EXPOSE 8788 4001
VOLUME /state
ENTRYPOINT ["/radchat"]
CMD ["relay"]
