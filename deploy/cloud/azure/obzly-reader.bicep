// Obzly cloud connection: read-only role assignments on one subscription
// (docs/INFRASTRUCTURE.md "Cloud connections"). The connection wizard fills
// these parameters in; deploy it yourself with:
//
//   az deployment sub create --location <region> --template-file obzly-reader.bicep \
//     --parameters accessMode=federated resourceGroupName=obzly issuer=https://obzly.example.com \
//       subject=probe:<probe group ID>
//
// For a management group, deploy the same role assignments at that scope
// (az deployment mg create). Every role is read-only.
targetScope = 'subscription'

@description('probe: the probe\'s own managed identity. workload: a new identity trusting the probe\'s Kubernetes service account. federated: a new identity trusting the Obzly control plane as issuer.')
@allowed(['probe', 'workload', 'federated'])
param accessMode string

@description('probe: the principal (object) ID of the probe\'s managed identity.')
param principalId string = ''

@description('workload / federated: the resource group for the new identity.')
param resourceGroupName string = ''

@description('workload: your AKS/EKS OIDC issuer URL. federated: the control plane URL, e.g. https://obzly.example.com.')
param issuer string = ''

@description('workload: system:serviceaccount:<namespace>:<service account>. federated: probe:<probe group ID>.')
param subject string = ''

param inventory bool = true
param cost bool = false
param posture bool = false

@description('cost: the storage account the FOCUS cost export writes to (empty without an export).')
param costExportStorageAccount string = ''

@description('cost: that storage account\'s resource group.')
param costExportResourceGroup string = ''

var createIdentity = accessMode != 'probe'

module identity 'obzly-identity.bicep' = if (createIdentity) {
  name: 'obzly-identity'
  scope: resourceGroup(resourceGroupName)
  params: {
    issuer: issuer
    subject: subject
  }
}

var principal = createIdentity ? identity!.outputs.principalId : principalId
// Assignment names must be known when the deployment starts, before the
// new identity exists.
var assignmentSeed = createIdentity ? '${resourceGroupName}/${subject}' : principalId

// Built-in role IDs: Reader, Cost Management Reader, Security Reader. Posture's baseline checks
// read resources' settings, which takes Reader too.
var roles = concat(
  inventory || posture ? ['acdd72a7-3385-48ef-bd42-f606fba81ae7'] : [],
  cost ? ['72fafb9e-0641-4937-9268-a91bfd8191a3'] : [],
  posture ? ['39bc4728-0917-49c7-9d2c-d95423bc2eb4'] : []
)

// Cost with an export: Storage Blob Data Reader on the export's storage account only.
module costExport 'obzly-cost-export.bicep' = if (cost && !empty(costExportStorageAccount)) {
  name: 'obzly-cost-export'
  scope: resourceGroup(costExportResourceGroup)
  params: {
    storageAccountName: costExportStorageAccount
    principalId: principal
    assignmentSeed: assignmentSeed
  }
}

resource assignments 'Microsoft.Authorization/roleAssignments@2022-04-01' = [for role in roles: {
  name: guid(subscription().id, assignmentSeed, role)
  properties: {
    principalId: principal
    principalType: 'ServicePrincipal'
    roleDefinitionId: subscriptionResourceId('Microsoft.Authorization/roleDefinitions', role)
  }
}]

@description('Paste into the wizard\'s "Client ID" (not needed for the probe\'s own identity).')
output clientId string = createIdentity ? identity!.outputs.clientId : ''
output tenantId string = subscription().tenantId
