import { apacheDataSource } from './apacheDataSource';
import { browserJsSdkDataSource } from './browserJsSdkDataSource';
import { cassandraDataSource } from './cassandraDataSource';
import { claudeCodeDataSource } from './claudeCodeDataSource';
import { consulDataSource } from './consulDataSource';
import { couchdbDataSource } from './couchdbDataSource';
import { cppSdkDataSource } from './cppSdkDataSource';
import { customOtlpDataSource } from './customOtlpDataSource';
import { dockerDataSource } from './dockerDataSource';
import { dotnetSdkDataSource } from './dotnetSdkDataSource';
import { elasticsearchDataSource } from './elasticsearchDataSource';
import { envoyDataSource } from './envoyDataSource';
import { erlangSdkDataSource } from './erlangSdkDataSource';
import { etcdDataSource } from './etcdDataSource';
import { flinkDataSource } from './flinkDataSource';
import { fluentbitDataSource } from './fluentbitDataSource';
import { goSdkDataSource } from './goSdkDataSource';
import { haproxyDataSource } from './haproxyDataSource';
import { httpCheckDataSource } from './httpCheckDataSource';
import { iisDataSource } from './iisDataSource';
import { javaSdkDataSource } from './javaSdkDataSource';
import { jvmDataSource } from './jvmDataSource';
import { kafkaDataSource } from './kafkaDataSource';
import { kotlinSdkDataSource } from './kotlinSdkDataSource';
import { kubernetesDataSource } from './kubernetesDataSource';
import { linuxDataSource } from './linuxDataSource';
import { llmAppDataSource } from './llmAppDataSource';
import { memcachedDataSource } from './memcachedDataSource';
import { mongodbDataSource } from './mongodbDataSource';
import { mysqlDataSource } from './mysqlDataSource';
import { nginxDataSource } from './nginxDataSource';
import { nodejsSdkDataSource } from './nodejsSdkDataSource';
import { obiDataSource } from './obiDataSource';
import { otelOperatorDataSource } from './otelOperatorDataSource';
import { phpSdkDataSource } from './phpSdkDataSource';
import { postgresqlDataSource } from './postgresqlDataSource';
import { prometheusDataSource } from './prometheusDataSource';
import { pythonSdkDataSource } from './pythonSdkDataSource';
import { rabbitmqDataSource } from './rabbitmqDataSource';
import { redisDataSource, valkeyDataSource } from './redisDataSource';
import { riakDataSource } from './riakDataSource';
import { rubySdkDataSource } from './rubySdkDataSource';
import { rustSdkDataSource } from './rustSdkDataSource';
import { snmpDataSource } from './snmpDataSource';
import { sparkDataSource } from './sparkDataSource';
import { sqlserverDataSource } from './sqlserverDataSource';
import { statsdDataSource } from './statsdDataSource';
import { swiftSdkDataSource } from './swiftSdkDataSource';
import { syslogDataSource } from './syslogDataSource';
import { traefikDataSource } from './traefikDataSource';
import type { DataSourceCategory, DataSourceDefinition } from './types';
import { vcenterDataSource } from './vcenterDataSource';
import { windowsDataSource } from './windowsDataSource';
import { zookeeperDataSource } from './zookeeperDataSource';

// New sources: add the definition file, then list it here - the catalog
// page/modal are both fully generic and need no changes.
export const dataSourceCatalog: DataSourceDefinition[] = [
  kubernetesDataSource,
  obiDataSource,
  dockerDataSource,
  linuxDataSource,
  windowsDataSource,
  syslogDataSource,
  snmpDataSource,
  zookeeperDataSource,
  vcenterDataSource,
  consulDataSource,
  etcdDataSource,
  nginxDataSource,
  apacheDataSource,
  haproxyDataSource,
  traefikDataSource,
  envoyDataSource,
  iisDataSource,
  postgresqlDataSource,
  mysqlDataSource,
  mongodbDataSource,
  redisDataSource,
  valkeyDataSource,
  elasticsearchDataSource,
  cassandraDataSource,
  couchdbDataSource,
  memcachedDataSource,
  sqlserverDataSource,
  riakDataSource,
  rabbitmqDataSource,
  kafkaDataSource,
  javaSdkDataSource,
  dotnetSdkDataSource,
  pythonSdkDataSource,
  nodejsSdkDataSource,
  browserJsSdkDataSource,
  goSdkDataSource,
  phpSdkDataSource,
  rubySdkDataSource,
  rustSdkDataSource,
  cppSdkDataSource,
  erlangSdkDataSource,
  swiftSdkDataSource,
  kotlinSdkDataSource,
  otelOperatorDataSource,
  jvmDataSource,
  flinkDataSource,
  sparkDataSource,
  claudeCodeDataSource,
  llmAppDataSource,
  customOtlpDataSource,
  prometheusDataSource,
  statsdDataSource,
  httpCheckDataSource,
  fluentbitDataSource,
];

export const dataSourceCategoryOrder: DataSourceCategory[] = [
  'Infrastructure',
  'Web Servers',
  'Databases',
  'Message Queues',
  'Languages',
  'Application Runtimes',
  'AI Tools',
  'Custom',
];

export function groupDataSourcesByCategory(
  sources: DataSourceDefinition[],
): Map<DataSourceCategory, DataSourceDefinition[]> {
  const grouped = new Map<DataSourceCategory, DataSourceDefinition[]>();
  for (const source of sources) {
    const existing = grouped.get(source.category) ?? [];
    existing.push(source);
    grouped.set(source.category, existing);
  }
  return grouped;
}
