FROM golang:latest as stage1

WORKDIR /go/src/lasso

COPY . .

RUN apt-get update
RUN apt-get install -y git
#RUN apk add openssl
#RUN apk add --no-cache curl g++ pkgconfig git openssl-dev
#RUN apt-get install -y --no-cache curl g++ git openssl-dev
#RUN apt-get install -y libssl1.1

RUN wget https://github.com/neo4j-drivers/seabolt/releases/download/v1.7.4/seabolt-1.7.4-Linux-ubuntu-18.04.deb
#RUN tar zxvf ~/Downloads/seabolt.tar.gz --strip-components=1 -C /seabolt-1.7.4-Linux-ubuntu-18.04.deb
RUN dpkg -i seabolt-1.7.4-Linux-ubuntu-18.04.deb

# Install latest seabolt
# RUN curl -s https://api.github.com/repos/neo4j-drivers/seabolt/releases/latest \
# 	| grep browser_download_url \
# 	| grep alpine-3.8 \
# 	| grep -v sha256 \
# 	| cut -d '"' -f 4 \
# 	| xargs curl -qL -o - \
# 	| tar -C / -zxv --strip-components=1
RUN go get -d -v ./...
RUN go install -v ./...
RUN go build lasso
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
