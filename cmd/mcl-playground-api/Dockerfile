FROM golang:1.27.2-alpine@sha256:f92b6ef800e499660581efdabdf25d9d817a9d124eaf900924f0504e7e27e12d AS build

WORKDIR /src
RUN apk add --no-cache ca-certificates

COPY go.mod go.sum ./
RUN go mod download

COPY cmd/mcl-playground-api/ ./cmd/mcl-playground-api/
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/mcl-playground-api ./cmd/mcl-playground-api

FROM scratch

COPY --from=build /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/
COPY --from=build /out/mcl-playground-api /mcl-playground-api

USER 65532:65532
EXPOSE 8080
ENTRYPOINT ["/mcl-playground-api"]
