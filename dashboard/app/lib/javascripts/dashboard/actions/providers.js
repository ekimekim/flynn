import { extend } from 'marbles/utils';
import Dispatcher from 'dashboard/dispatcher';
import Config from 'dashboard/config';

var provisionResource = function (providerID) {
	var client = Config.client;
	client.provisionResource(providerID);
};

var copyResourceEnvToApp = function (appID, resource) {
	var client = Config.client;
	var resourceEnv = resource.env || {};
	client.getAppRelease(appID).then(function (args) {
		var release = extend({}, args[0]);
		release.env = release.env || {};
		Object.keys(resourceEnv).forEach(function (k) {
			release.env[k] = resourceEnv[k];
		});
		delete release.id;
		delete release.created_at;
		return client.createRelease(release).then(function (args) {
			var release = args[0];
			return client.deployAppRelease(appID, release.id);
		});
	});
};

var provisionResourcesForApp = function (providerIDs, appID) {
	var client = Config.client;
	providerIDs.forEach(function (providerID) {
		client.provisionResource(providerID, {
			apps: [appID]
		}).then(function (args) {
			// TODO(jvatic): Add error handling such that it shows in UI
			var resource = args[0];
			return copyResourceEnvToApp(appID, resource);
		});
	});
};

var addAppToResource = function (appID, providerID, resourceID) {
	// TODO(jvatic): Add error handling such that it shows in UI
	var client = Config.client;
	client.getResource(providerID, resourceID).then(function (args) {
		var resource = args[0];
		return copyResourceEnvToApp(appID, resource);
	}).then(function () {
		return client.addResourceApp(providerID, resourceID, appID);
	});
};

var deleteResource = function (providerID, resourceID, appID) {
	var client = Config.client;
	var shouldDeleteResource = true;
	client.getResource(providerID, resourceID).then(function (args) {
		// remove resource env from all associated apps or just appID if given
		var resource = args[0];
		var resourceEnv = resource.env || {};
		var appIDs = appID ? [appID] : resource.apps || [];
		if (appID && (resource.apps || []).length > 1) {
			shouldDeleteResource = false;
		}
		return Promise.all(appIDs.map(function (appID) {
			return client.getAppRelease(appID).then(function (args) {
				var release = extend({}, args[0]);
				var newReleaseEnv = extend({}, release.env || {});
				Object.keys(resourceEnv).forEach(function (k) {
					if (newReleaseEnv[k] === resourceEnv[k]) {
						delete newReleaseEnv[k];
					}
				});
				release.env = newReleaseEnv;
				delete release.id;
				delete release.created_at;

				return client.createRelease(release).then(function (args) {
					var release = args[0];
					return client.deployAppRelease(appID, release.id);
				});
			}).catch(function () {
				// app doesn't have a release, ignore
			});
		})).then(function () {
			if (shouldDeleteResource) {
				return;
			}
			// the resource has other apps using it
			// so remove appID from resource.apps instead of deleting it
			return client.deleteResourceApp(providerID, resourceID, appID);
		});
	}).then(function () {
		if (shouldDeleteResource) {
			return client.deleteResource(providerID, resourceID);
		}
	});
};

var createExternalProviderRoute = function (providerAppID, serviceName) {
	var client = Config.client;
	client.createAppRoute(providerAppID, {
		type: 'tcp',
		leader: true,
		service: serviceName
	});
};

Dispatcher.register(function (event) {
	switch (event.name) {
	case 'PROVISION_RESOURCE_WITH_ROUTE':
		if (event.resourceID) {
			Config.history.navigate('/providers/'+ event.providerID +'/resources/'+ event.resourceID +'/create-external-route?provision=true', { replace: true });
		} else {
			Config.history.navigate('/providers/'+ event.providerID +'/create-external-route?provision=true', { replace: true });
		}
		break;
	case 'PROVISION_RESOURCE':
		provisionResource(event.providerID);
		break;
	case 'APP_PROVISION_RESOURCES':
		provisionResourcesForApp(event.providerIDs, event.appID);
		break;
	case 'RESOURCE_ADD_APP':
		addAppToResource(event.appID, event.providerID, event.resourceID);
		break;
	case 'DELETE_RESOURCE':
		deleteResource(event.providerID, event.resourceID, event.appID);
		break;
	case 'CREATE_EXTERNAL_PROVIDER_ROUTE':
		createExternalProviderRoute(event.providerAppID, event.serviceName);
		break;
	}
});
