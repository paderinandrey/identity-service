FROM golang:1.27.1 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -o /out/stub-consumer .

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/stub-consumer /stub-consumer
EXPOSE 8080
ENTRYPOINT ["/stub-consumer"]
