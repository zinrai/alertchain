FROM golang:1.26-trixie AS build

WORKDIR /src

# Download modules first so this layer is cached until go.mod/go.sum
# change.
COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 go build -ldflags '-s -w' -o /out/alertchain ./cmd/alertchain

FROM debian:trixie-slim

RUN useradd --system --no-create-home --uid 10001 alertchain

COPY --from=build /out/alertchain /usr/local/bin/alertchain

USER alertchain
EXPOSE 9093

ENTRYPOINT ["/usr/local/bin/alertchain"]
CMD ["serve", "--config", "/etc/alertchain/alertchain.yaml", "--listen", ":9093"]
