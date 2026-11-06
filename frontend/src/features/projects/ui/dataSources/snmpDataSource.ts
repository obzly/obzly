import {
  BATCH_PROCESSOR_YAML,
  MEMORY_LIMITER_PROCESSOR_YAML,
  OTLP_REMOTE_FIELD,
  RESOURCEDETECTION_PROCESSOR_YAML,
  extensionsYaml,
  otlpHttpExporterYaml,
  otlpReceiverYaml,
  serviceExtensionsYaml,
} from './otelCollectorCommon';
import type { DataSourceDefinition } from './types';

export const snmpDataSource: DataSourceDefinition = {
  id: 'snmp',
  name: 'SNMP (network devices)',
  category: 'Infrastructure',
  description: 'Interface/system metrics from routers, switches, and other SNMP-capable devices.',
  fields: [
    { key: 'endpoint', label: 'Device address', defaultValue: 'udp://192.168.1.1:161' },
    { key: 'community', label: 'SNMP community string (v2c)', defaultValue: 'public' },
    OTLP_REMOTE_FIELD,
  ],
  build: ({ baseUrl, apiKey, fieldValues }) => {
    const endpoint = fieldValues.endpoint.trim() || 'udp://192.168.1.1:161';
    const community = fieldValues.community.trim() || 'public';

    const content = `# Obzly OpenTelemetry Collector config for SNMP-capable network devices
# (routers, switches, firewalls, etc.).
#
# Uses SNMPv2c with a community string by default - change to v3 with
# proper auth/priv settings for anything internet-facing or production.
# The receiver has no built-in metric set, so each metric is defined
# below by its OID: IF-MIB interface octets/packets/errors/discards
# (per direction) and oper status, labelled by ifName, plus SNMPv2-MIB
# sysUpTime. Tested against net-snmp 5.9.

receivers:
  snmp:
    endpoint: "${endpoint}"
    version: v2c
    community: ${community}
    collection_interval: 30s
    resource_attributes:
      snmp.interface.name:
        oid: "1.3.6.1.2.1.31.1.1.1.1"
    attributes:
      direction:
        enum: [receive, transmit]
    metrics:
      snmp.interface.io:
        description: Octets through the interface (IF-MIB ifHCIn/OutOctets).
        unit: By
        sum:
          aggregation: cumulative
          monotonic: true
          value_type: int
        column_oids:
          - oid: "1.3.6.1.2.1.31.1.1.1.6"
            resource_attributes: [snmp.interface.name]
            attributes: [{name: direction, value: receive}]
          - oid: "1.3.6.1.2.1.31.1.1.1.10"
            resource_attributes: [snmp.interface.name]
            attributes: [{name: direction, value: transmit}]
      snmp.interface.packets:
        description: Unicast packets (IF-MIB ifHCIn/OutUcastPkts).
        unit: "{packet}"
        sum:
          aggregation: cumulative
          monotonic: true
          value_type: int
        column_oids:
          - oid: "1.3.6.1.2.1.31.1.1.1.7"
            resource_attributes: [snmp.interface.name]
            attributes: [{name: direction, value: receive}]
          - oid: "1.3.6.1.2.1.31.1.1.1.11"
            resource_attributes: [snmp.interface.name]
            attributes: [{name: direction, value: transmit}]
      snmp.interface.errors:
        description: Packets with errors (IF-MIB ifIn/OutErrors).
        unit: "{packet}"
        sum:
          aggregation: cumulative
          monotonic: true
          value_type: int
        column_oids:
          - oid: "1.3.6.1.2.1.2.2.1.14"
            resource_attributes: [snmp.interface.name]
            attributes: [{name: direction, value: receive}]
          - oid: "1.3.6.1.2.1.2.2.1.20"
            resource_attributes: [snmp.interface.name]
            attributes: [{name: direction, value: transmit}]
      snmp.interface.discards:
        description: Discarded packets (IF-MIB ifIn/OutDiscards).
        unit: "{packet}"
        sum:
          aggregation: cumulative
          monotonic: true
          value_type: int
        column_oids:
          - oid: "1.3.6.1.2.1.2.2.1.13"
            resource_attributes: [snmp.interface.name]
            attributes: [{name: direction, value: receive}]
          - oid: "1.3.6.1.2.1.2.2.1.19"
            resource_attributes: [snmp.interface.name]
            attributes: [{name: direction, value: transmit}]
      snmp.interface.oper_status:
        description: "Operational status (IF-MIB ifOperStatus: 1 up, 2 down, 3 testing, ...)."
        unit: "1"
        gauge:
          value_type: int
        column_oids:
          - oid: "1.3.6.1.2.1.2.2.1.8"
            resource_attributes: [snmp.interface.name]
      snmp.system.uptime:
        description: Time since the device's network management was reinitialized (sysUpTime).
        unit: "cs"
        gauge:
          value_type: int
        scalar_oids:
          - oid: "1.3.6.1.2.1.1.3.0"

${otlpReceiverYaml(fieldValues)}

processors:
${MEMORY_LIMITER_PROCESSOR_YAML}
${BATCH_PROCESSOR_YAML}
${RESOURCEDETECTION_PROCESSOR_YAML}

${extensionsYaml()}

exporters:
${otlpHttpExporterYaml(baseUrl)}

service:
${serviceExtensionsYaml()}
  pipelines:
    metrics:
      receivers: [snmp, otlp]
      processors: [memory_limiter, resource_detection, batch]
      exporters: [otlp_http]
    logs:
      receivers: [otlp]
      processors: [memory_limiter, resource_detection, batch]
      exporters: [otlp_http]
    traces:
      receivers: [otlp]
      processors: [memory_limiter, resource_detection, batch]
      exporters: [otlp_http]
`;

    return {
      filename: 'otel-collector-config.yaml',
      language: 'yaml',
      content,
      instructions: `Run with: OBZLY_API_KEY="${apiKey}" otelcol-contrib --config=otel-collector-config.yaml. For syslog from the same device, also set up the Syslog source.`,
    };
  },
};
