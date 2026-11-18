import { describe, expect, it } from 'vitest';

import {
  cloudIcon,
  dimensionIcon,
  guessProvider,
  regionProvider,
  standardProvider,
} from './cloudIcons';

const icon = (name: string) => `/icons/infra/${name}.svg`;

describe('cloudIcon', () => {
  it('names AWS services from billing and FOCUS names', () => {
    expect(cloudIcon('Amazon Elastic Compute Cloud - Compute').icon).toBe(icon('aws-ec2'));
    expect(cloudIcon('EC2 - Other').icon).toBe(icon('aws-ec2'));
    expect(cloudIcon('Amazon Simple Storage Service').icon).toBe(icon('aws-s3'));
    expect(cloudIcon('Amazon Relational Database Service').icon).toBe(icon('aws-rds'));
    expect(cloudIcon('AWS Lambda').icon).toBe(icon('aws-lambda'));
    expect(cloudIcon('Amazon EC2 Auto Scaling').icon).toBe(icon('aws-autoscaling'));
    expect(cloudIcon('Amazon Elastic Kubernetes Service').icon).toBe(icon('aws-eks'));
  });

  it('names Azure services, scale sets before virtual machines', () => {
    expect(cloudIcon('Virtual Machines').icon).toBe(icon('azure-vm'));
    expect(cloudIcon('Virtual Machine Scale Sets').icon).toBe(icon('azure-vmss'));
    expect(cloudIcon('Storage').icon).toBe(icon('azure-storage'));
    expect(cloudIcon('Azure Database for PostgreSQL').icon).toBe(icon('azure-postgresql'));
  });

  it('reads the type from ARNs, resource IDs and resource types, not the name', () => {
    expect(cloudIcon('arn:aws:lambda:eu-west-1:123456789012:function:vm-cleanup').icon).toBe(
      icon('aws-lambda'),
    );
    expect(
      cloudIcon(
        '/subscriptions/0000/resourceGroups/rg/providers/Microsoft.Compute/virtualMachineScaleSets/web',
      ).icon,
    ).toBe(icon('azure-vmss'));
    expect(cloudIcon('aws_rds_instance').icon).toBe(icon('aws-rds'));
    expect(cloudIcon('AwsEc2Instance').icon).toBe(icon('aws-ec2'));
    expect(cloudIcon('Microsoft.ContainerService/managedClusters').icon).toBe(icon('azure-aks'));
  });

  it('falls back to the provider mark, and to nothing when the cloud is unknown', () => {
    expect(cloudIcon('Amazon Route 53')).toEqual({ provider: 'aws', label: 'AWS' });
    expect(cloudIcon('Key Vault', 'azure')).toEqual({
      provider: 'azure',
      label: 'Microsoft Azure',
    });
    expect(cloudIcon('Tax')).toEqual({});
  });
});

describe('providers', () => {
  it('tells accounts, regions and standards apart', () => {
    expect(guessProvider('123456789012')).toBe('aws');
    expect(guessProvider('3f2a9c1e-1111-4a2b-9c3d-123456789abc')).toBe('azure');
    expect(regionProvider('us-east-1')).toBe('aws');
    expect(regionProvider('westeurope')).toBe('azure');
    expect(regionProvider('global')).toBeUndefined();
    expect(dimensionIcon('charge', 'Usage')).toEqual({});
    expect(dimensionIcon('provider', 'azure').provider).toBe('azure');
    expect(standardProvider('cis-aws')).toBe('aws');
    expect(standardProvider('mcsb')).toBe('azure');
    expect(standardProvider('obzly-baseline')).toBeUndefined();
  });
});
