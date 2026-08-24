FROM golang:1.27-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -o /out/seekout .

FROM alpine:3.21
RUN apk add --no-cache ca-certificates tzdata
COPY --from=build /out/seekout /seekout
COPY entrypoint.sh /entrypoint.sh
RUN chmod +x /entrypoint.sh
EXPOSE 8080
VOLUME /pb/pb_data
ENTRYPOINT ["/entrypoint.sh"]
CMD ["/seekout", "serve", "--http=0.0.0.0:8080", "--dir=/pb/pb_data"]
