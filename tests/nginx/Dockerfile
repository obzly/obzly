# Fake Nginx log generator - writes synthetic access/error log lines to
# the same default paths the Nginx data source's collector config expects
# (/var/log/nginx/{access,error}.log), so you can test log ingestion
# end-to-end without running real Nginx or driving real traffic at it.
#
# See ../README.md for how to wire this up with the OTel Collector config
# generated from Obzly's Data Sources page.
FROM alpine:3.20

RUN apk add --no-cache bash coreutils

COPY generate-fake-logs.sh /usr/local/bin/generate-fake-logs.sh
RUN chmod +x /usr/local/bin/generate-fake-logs.sh

# Overridable: ACCESS_LOG_PATH, ERROR_LOG_PATH, LOG_INTERVAL_SECONDS.
ENTRYPOINT ["/usr/local/bin/generate-fake-logs.sh"]
