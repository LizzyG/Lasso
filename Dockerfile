FROM golang:latest as stage1

WORKDIR /Users/lizg/go/src/github.com/lizzyg/lasso

COPY . .

RUN apt-get update
RUN apt-get install -y git

RUN wget https://github.com/neo4j-drivers/seabolt/releases/download/v1.7.4/seabolt-1.7.4-Linux-ubuntu-18.04.deb
RUN dpkg -i seabolt-1.7.4-Linux-ubuntu-18.04.deb

RUN go get -d -v ./...
RUN go install -v ./...
#RUN go build lasso
RUN go build .
RUN apt-get install -y vim
RUN apt-get install -y less
EXPOSE 8080
ENV PORT 8080
ENV IP 52.11.117.0
ENTRYPOINT /go/src/lasso/lasso

#CMD /bin/sh

# FROM golang:1.12-alpine
# WORKDIR /lasso
# COPY --from=stage1 /go/src/lasso /lasso/ 
# RUN apk update
# RUN apk add vim
# RUN apk add less
# EXPOSE 8080
# ENV PORT 8080
# ENV IP 52.11.117.0
# ENTRYPOINT /lasso/lasso
