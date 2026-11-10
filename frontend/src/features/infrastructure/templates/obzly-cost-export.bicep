// Obzly cloud connection, cost use: read access to the storage account the FOCUS cost export
// writes to (Storage Blob Data Reader), deployed by obzly-reader.bicep into its resource group.

@description('The storage account the cost export writes to.')
param storageAccountName string

@description('The principal (object) ID Obzly signs in as.')
param principalId string

@description('Keeps the assignment name stable across deployments.')
param assignmentSeed string

var storageBlobDataReader = '2a2b9908-6ea1-4ae2-8e65-a410df84e7d1'

resource account 'Microsoft.Storage/storageAccounts@2023-05-01' existing = {
  name: storageAccountName
}

resource reader 'Microsoft.Authorization/roleAssignments@2022-04-01' = {
  name: guid(account.id, assignmentSeed, storageBlobDataReader)
  scope: account
  properties: {
    principalId: principalId
    principalType: 'ServicePrincipal'
    roleDefinitionId: subscriptionResourceId('Microsoft.Authorization/roleDefinitions', storageBlobDataReader)
  }
}
