FROM golang:1.23-alpine 


WORKDIR /course-crd-operator

# Copy the Go Modules manifests
COPY go.mod go.mod
COPY go.sum go.sum
# cache deps before building and copying source so that we don't need to re-download as much
# and so that source changes don't invalidate our downloaded layer
RUN go mod download


COPY main.go main.go
COPY pkg/ pkg/



# Build
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -a -o bin/course-operator


FROM alpine:3.21
RUN apk add --no-cache tzdata 
ENV TZ=Asia/Taipei
RUN ln -snf /usr/share/zoneinfo/$TZ /etc/localtime
RUN mkdir -p /etc/api-server
COPY ./zoneinfo.zip /usr/local/go/lib/time/

COPY --from=0 /course-crd-operator/bin/course-operator .

CMD ["/course-operator", "--logtostderr=true"]