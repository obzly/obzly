import { describe, expect, it } from 'vitest';

import { buildKubernetesOtelConfig } from './kubernetesOtelConfig';

const manifest = buildKubernetesOtelConfig({
  baseUrl: 'https://obzly.example.com',
  apiKey: 'test-key',
  namespace: 'obzly',
  clusterName: 'c1',
});

describe('buildKubernetesOtelConfig', () => {
  it('checkpoints file_log offsets in file_storage on the node', () => {
    expect(manifest).toContain(
      '      file_storage:\n        directory: /var/lib/otelcol/file_storage',
    );
    expect(manifest).toMatch(/file_log:[\s\S]*?storage: file_storage/);
    expect(manifest).toContain('      extensions: [health_check, file_storage]');
  });

  it('mounts a hostPath for the offsets and makes it writable for the collector user', () => {
    expect(manifest).toMatch(
      /- name: file-storage\n {10}hostPath:\n {12}path: \/var\/lib\/otelcol\n {12}type: DirectoryOrCreate/,
    );
    expect(manifest).toContain('chown -R 10001:10001 /var/lib/otelcol/file_storage');
    expect(manifest.match(/mountPath: \/var\/lib\/otelcol\n/g)).toHaveLength(2);
  });

  it("names each pod metric's workload for the inventory", () => {
    expect(manifest).toContain(
      'processors: [memory_limiter, resource_detection, resource/cluster_name, k8s_attributes, batch]\n          exporters: [otlp_http]\n---',
    );
    expect(manifest).toContain(
      '            - k8s.statefulset.name\n            - k8s.daemonset.name',
    );
    expect(manifest).toMatch(/apiGroups: \["batch"\]\n {4}resources: \["jobs"\]/);
  });

  it('Test_KubernetesManifest_LimitUtilization_IsOptInWithNodesProxy', () => {
    expect(manifest).not.toContain('limit_utilization');
    expect(manifest).not.toContain('nodes/proxy');

    const withLimits = buildKubernetesOtelConfig({
      baseUrl: 'https://obzly.example.com',
      apiKey: 'test-key',
      namespace: 'obzly',
      limitUtilization: true,
    });
    expect(withLimits).toContain('k8s.pod.memory_limit_utilization:\n            enabled: true');
    expect(withLimits).toMatch(/resources: \["nodes\/proxy"\]\n {4}verbs: \["get"\]/);
  });

  it('Test_KubernetesManifest_Containers_AreRestricted', () => {
    const contexts = manifest.match(/securityContext:\n(?: {12}.*\n)+/g) ?? [];
    // DaemonSet collector, its init container and the cluster agent.
    expect(contexts).toHaveLength(3);
    for (const context of contexts) {
      expect(context).toContain('allowPrivilegeEscalation: false');
      expect(context).toContain('readOnlyRootFilesystem: true');
      expect(context).toContain('drop: ["ALL"]');
      expect(context).toContain('type: RuntimeDefault');
    }
    expect(manifest.match(/runAsNonRoot: true/g)).toHaveLength(2);
    // Only the init container's chown runs as root, with just what it needs.
    expect(manifest).toContain('add: ["CHOWN", "DAC_OVERRIDE"]');
    expect(manifest.match(/- name: tmp\n {10}emptyDir: \{\}/g)).toHaveLength(2);
  });

  it('Test_KubernetesManifest_HostNetwork_BindsOTLPToPodIP', () => {
    expect(manifest).toContain('endpoint: 0.0.0.0:4317');
    const daemonSet = manifest.split('name: obzly-otel-cluster-agent')[0];
    expect(daemonSet).not.toContain('KUBE_POD_IP');

    const onHost = buildKubernetesOtelConfig({
      baseUrl: 'https://obzly.example.com',
      apiKey: 'test-key',
      namespace: 'obzly',
      hostNetwork: true,
    });
    expect(onHost).not.toContain('0.0.0.0');
    expect(onHost).toContain('endpoint: ${env:KUBE_POD_IP}:4317');
    expect(onHost).toContain('endpoint: ${env:KUBE_POD_IP}:4318');
    expect(onHost).toContain('endpoint: ${env:KUBE_POD_IP}:13133');
    expect(onHost).toMatch(
      /- name: KUBE_POD_IP\n {14}valueFrom:\n {16}fieldRef:\n {18}fieldPath: status\.podIP/,
    );
  });

  it('Test_KubernetesManifest_Collectors_HaveHealthChecksAndMemoryLimiter', () => {
    expect(manifest).toContain('      extensions: [health_check, file_storage]');
    expect(manifest).toContain('      extensions: [health_check]');
    // DaemonSet and cluster agent.
    expect(
      manifest.match(/livenessProbe:\n {12}httpGet:\n {14}path: \/\n {14}port: 13133/g),
    ).toHaveLength(2);
    expect(manifest.match(/readinessProbe:/g)).toHaveLength(2);
    expect(manifest.match(/limit_percentage: 80/g)).toHaveLength(2);
    const pipelines = manifest.match(/processors: \[[^\]]*\]/g) ?? [];
    expect(pipelines).toHaveLength(4);
    for (const pipeline of pipelines) {
      expect(pipeline).toMatch(/^processors: \[memory_limiter, .*batch\]$/);
    }
  });

  it('Test_KubernetesManifest_ResourceDetection_CoversEksGkeAks', () => {
    // Cluster detectors before their VM ones, so cloud.platform names the cluster service.
    expect(manifest).toContain(
      'detectors: [env, eks, ec2, gcp, aks, azure, system]\n        timeout: 5s',
    );
  });

  it('adds a read-only cluster agent unless turned off', () => {
    expect(manifest).toContain('kind: Deployment\nmetadata:\n  name: obzly-otel-cluster-agent');
    expect(manifest).toMatch(/k8s_cluster:\n {8}auth_type: serviceAccount/);
    // Host detectors would stamp every object with the agent's own node.
    expect(manifest).toMatch(/obzly-otel-cluster-agent[\s\S]*detectors: \[env, eks, aks\]\n/);
    const clusterRole = manifest.split('name: obzly-otel-cluster-agent\nrules:')[1].split('---')[0];
    expect(clusterRole).not.toMatch(/create|update|patch|delete/);

    const withoutAgent = buildKubernetesOtelConfig({
      baseUrl: 'https://obzly.example.com',
      apiKey: 'test-key',
      namespace: 'obzly',
      clusterAgent: false,
    });
    expect(withoutAgent).not.toContain('obzly-otel-cluster-agent');
  });

  // CI renders the manifest in its main shapes and validates them against
  // the Kubernetes schemas (ci-release.yml). The app's types are browser
  // only, hence Node's fs through globalThis and a dynamic import.
  const out = (globalThis as { process?: { env: Record<string, string | undefined> } }).process?.env
    .K8S_MANIFEST_OUT;
  it.skipIf(!out)('writes the manifests for schema validation', async () => {
    const fsModule: string = 'node:fs';
    const fs = await import(/* @vite-ignore */ fsModule);
    fs.mkdirSync(out, { recursive: true });
    const shapes = {
      default: { clusterName: 'c1' },
      filtered: { excludeNamespaces: 'kube-system', excludePods: 'noisy-*', hostNetwork: true },
      'no-cluster-agent': { clusterAgent: false },
      'limit-utilization': { limitUtilization: true },
    };
    for (const [name, options] of Object.entries(shapes)) {
      fs.writeFileSync(
        `${out}/${name}.yaml`,
        buildKubernetesOtelConfig({
          baseUrl: 'https://obzly.example.com',
          apiKey: 'test-key',
          namespace: 'obzly',
          ...options,
        }),
      );
    }
  });
});
