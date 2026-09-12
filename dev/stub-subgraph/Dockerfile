FROM golang:1.27.1 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -o /out/stub-subgraph .

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/stub-subgraph /stub-subgraph
EXPOSE 8080
ENTRYPOINT ["/stub-subgraph"]
