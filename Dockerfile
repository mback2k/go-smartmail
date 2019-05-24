FROM golang:alpine as build
RUN apk --no-cache --update upgrade && apk --no-cache add git build-base

ADD . /go/go-smartmail
WORKDIR /go/go-smartmail

RUN go get
RUN go build -ldflags="-s -w"
RUN chmod +x go-smartmail

FROM mback2k/alpine:latest
RUN apk --no-cache --update upgrade && apk --no-cache add ca-certificates

COPY --from=build /go/go-smartmail/go-smartmail /usr/local/bin/go-smartmail

RUN addgroup -g 993 -S serve
RUN adduser -u 993 -h /data -S -D -G serve serve

WORKDIR /data
USER serve

CMD [ "/usr/local/bin/go-smartmail" ]
