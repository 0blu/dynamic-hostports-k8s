FROM golang:alpine as builder
WORKDIR /src
COPY src .
RUN go build -o main .

FROM alpine
RUN adduser --system --no-create-home user
USER user

WORKDIR /app
COPY --from=builder /src/main /app/
CMD ["./main"]
