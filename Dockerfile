FROM golang:1.18 as builder

# docker login --username=micro_service_governance@test.aliyunid.com registry.cn-hangzhou.aliyuncs.com
# docker build -t registry.cn-hangzhou.aliyuncs.com/mse-demo-hz/dubbo-demo:client-go-1.0.0 .

WORKDIR /app

COPY . .

RUN CGO_ENABLED=0 go build -o ingress-manager main.go

FROM alpine:3.15.3

WORKDIR /app

COPY --from=builder /app/ingress-manager .


CMD ["./ingress-manager"]
