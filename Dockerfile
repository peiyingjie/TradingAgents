FROM golang:1.25-bookworm AS builder

ENV CGO_ENABLED=0

WORKDIR /build
COPY go.mod go.sum ./
RUN go mod download
COPY cmd ./cmd
COPY internal ./internal
COPY pkg ./pkg
RUN go build -trimpath -o /out/tradingagents ./cmd/tradingagents

FROM debian:bookworm-slim

RUN apt-get update \
 && apt-get install -y --no-install-recommends ca-certificates tzdata \
 && rm -rf /var/lib/apt/lists/*

RUN useradd --create-home appuser \
 && install -d -m 0755 -o appuser -g appuser /home/appuser/.tradingagents /home/appuser/app
ENV HOME=/home/appuser
COPY --from=builder /out/tradingagents /usr/local/bin/tradingagents
USER appuser
WORKDIR /home/appuser/app

ENTRYPOINT ["tradingagents"]
