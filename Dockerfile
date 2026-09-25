FROM --platform=$BUILDPLATFORM node:24 AS builder

WORKDIR /web
COPY ./VERSION .
COPY ./web .

RUN npm ci --legacy-peer-deps --prefix /web/default
RUN npm ci --legacy-peer-deps --prefix /web/berry
RUN npm ci --legacy-peer-deps --prefix /web/air

RUN REACT_APP_VERSION=$(cat ./VERSION) npm run build --prefix /web/default
RUN REACT_APP_VERSION=$(cat ./VERSION) npm run build --prefix /web/berry
RUN REACT_APP_VERSION=$(cat ./VERSION) npm run build --prefix /web/air

FROM golang:1.27.1-alpine AS builder2

RUN apk add --no-cache \
    gcc \
    musl-dev \
    sqlite-dev \
    build-base

ENV GO111MODULE=on \
    CGO_ENABLED=1 \
    GOOS=linux

WORKDIR /build

ADD go.mod go.sum ./
RUN go mod download

COPY . .
COPY --from=builder /web/build ./web/build

RUN go build -trimpath -ldflags "-s -w -X 'github.com/infinmalum/one-gateway/common.Version=$(cat VERSION)' -linkmode external -extldflags '-static'" -o one-gateway

FROM alpine:latest

RUN apk add --no-cache ca-certificates tzdata

COPY --from=builder2 /build/one-gateway /

EXPOSE 3000
WORKDIR /data
ENTRYPOINT ["/one-gateway"]
