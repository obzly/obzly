# Fake MySQL log generator - writes synthetic MySQL 8.x error-log lines to
# the default path the MySQL data source's collector config expects
# (/var/log/mysql/error.log), so you can test log ingestion end-to-end
# without running a real mysqld.
#
# See ../README.md for how to wire this up with the OTel Collector config
# generated from Obzly's Data Sources page.
FROM alpine:3.20

RUN apk add --no-cache bash coreutils

COPY generate-fake-logs.sh /usr/local/bin/generate-fake-logs.sh
RUN chmod +x /usr/local/bin/generate-fake-logs.sh

# Overridable: LOG_PATH, LOG_INTERVAL_SECONDS.
ENTRYPOINT ["/usr/local/bin/generate-fake-logs.sh"]
