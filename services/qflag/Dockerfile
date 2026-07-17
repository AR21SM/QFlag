FROM golang:1.23-alpine AS build
WORKDIR /src
COPY go.mod ./
COPY cmd ./cmd
COPY internal ./internal
RUN go test ./... && go build -o /out/qflag ./cmd/qflag

FROM alpine:3.20
RUN adduser -D -H appuser
USER appuser
WORKDIR /app
COPY --from=build /out/qflag /app/qflag
EXPOSE 8080
ENTRYPOINT ["/app/qflag"]
