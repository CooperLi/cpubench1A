FROM golang as builder
WORKDIR /go/src/github.com/AmadeusITGroup/cpubench1A/
COPY . .
RUN CGO_ENABLE=0 go build -ldflags="-extldflags=-static" -o /go/bin/bench && strip /go/bin/bench

FROM scratch
COPY --from=builder /go/bin/bench .
ENTRYPOINT ["./bench"]
