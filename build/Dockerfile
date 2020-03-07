FROM golang:1.12-alpine as stage1

WORKDIR /go/src/lasso

COPY . .

RUN apk update
RUN apk add git
RUN go get -d -v ./...
RUN go install -v ./...
RUN go build lasso


FROM golang:1.12-alpine
WORKDIR /lasso
COPY --from=stage1 /go/src/lasso /lasso/ 
RUN apk update
RUN apk add vim
RUN apk add less
EXPOSE 8080
ENV PORT 8080
ENV IP 52.11.117.0
ENTRYPOINT /lasso/lasso