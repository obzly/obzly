// A user-assigned identity Obzly's probe signs in as, through a federated
// credential (no secret): used by obzly-reader.bicep.
param issuer string
param subject string
param name string = 'obzly-readonly'
param location string = resourceGroup().location

resource identity 'Microsoft.ManagedIdentity/userAssignedIdentities@2023-01-31' = {
  name: name
  location: location
}

resource credential 'Microsoft.ManagedIdentity/userAssignedIdentities/federatedIdentityCredentials@2023-01-31' = {
  parent: identity
  name: 'obzly-probe'
  properties: {
    issuer: issuer
    subject: subject
    audiences: ['api://AzureADTokenExchange']
  }
}

output principalId string = identity.properties.principalId
output clientId string = identity.properties.clientId
