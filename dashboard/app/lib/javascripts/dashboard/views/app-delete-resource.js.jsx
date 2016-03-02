import Config from 'dashboard/config';
import Dispatcher from 'dashboard/dispatcher';
import Modal from 'Modal';
import ProvidersStore from 'dashboard/stores/providers';
import ResourcesStore from 'dashboard/stores/resources';
import AppsStore from 'dashboard/stores/apps';

var providersStoreID = 'default';
var resourcesStoreID = 'default';
var appsStoreID = null;

var AppDeleteResource = React.createClass({
	displayName: "Views.AppDeleteResource",

	render: function () {
		var pAttrs = this.state.providerAttrs;
		var app = this.state.app;
		var otherResourceApps = this.state.otherResourceApps;
		var isDeleting = this.state.isDeleting;

		return (
			<Modal onShow={function(){}} onHide={this.props.onHide} visible={true}>
				<section className="app-delete-resource">
					<header>
						<h1>Remove {pAttrs.title}</h1>
					</header>

					{otherResourceApps.length ? (
						<p className="alert-info">
							This will unlink {app.name} from {pAttrs.title}. All data will remain intact for use by the following apps:<br />
							<ul>
								{otherResourceApps.map(function (otherApp) {
									return (
										<li key={otherApp.id}>
											<RouteLink path={'/apps/'+ otherApp.id}>{otherApp.name}</RouteLink>
										</li>
									);
								})}
							</ul>
							<br/>
							<br/>
							You may delete the resource directly <RouteLink path={'/providers/'+ this.props.providerID +'/resources/'+ this.props.resourceID +'/delete'}>here</RouteLink>.
						</p>
					) : (
						<p className="alert-warning">This will remove {pAttrs.title} from {app.name}. Any data stored for this app will be destroyed.</p>
					)}

					<button disabled={isDeleting} className="delete-btn" onClick={this.__handleDeleteBtnClick}>
						{isDeleting ? 'Please wait...' : 'I understand, remove '+ pAttrs.title}
					</button>
				</section>
			</Modal>
		);
	},

	__handleDeleteBtnClick: function (e) {
		e.preventDefault();
		Dispatcher.dispatch({
			name: 'DELETE_RESOURCE',
			appID: this.props.appID,
			providerID: this.props.providerID,
			resourceID: this.props.resourceID
		});
	},

	getInitialState: function () {
		return this.__getState(this.props);
	},

	__getState: function (props) {
		var state = {};

		var providersState = ProvidersStore.getState(providersStoreID);
		var provider = providersState.providers.find(function (provider) {
			return provider.id === props.providerID;
		});
		state.providerAttrs = provider ? Config.PROVIDER_ATTRS[provider.name] : {title: ''};

		state.isDeleting = (providersState.deletingResourceStates[props.resourceID] || {}).isDeleting || false;

		var appsState = AppsStore.getState(appsStoreID);
		var appsByID = {};
		appsState.apps.forEach(function (app) {
			appsByID[app.id] = app;
		});
		state.app = appsByID[props.appID] || {name: ''};

		var resourcesState = ResourcesStore.getState(resourcesStoreID);
		state.otherResourceApps = ((resourcesState.resources.find(function (resource) {
			return resource.id === props.resourceID;
		}) || {}).apps || []).filter(function (appID) {
			return appID !== props.appID;
		}).map(function (appID) {
			return appsByID[appID] || {name: ''};
		});

		return state;
	},

	componentDidMount: function () {
		ProvidersStore.addChangeListener(providersStoreID, this.__handleStoreChange);
		ResourcesStore.addChangeListener(resourcesStoreID, this.__handleStoreChange);
		AppsStore.addChangeListener(appsStoreID, this.__handleStoreChange);
	},

	componentWillUnmount: function () {
		ProvidersStore.removeChangeListener(providersStoreID, this.__handleStoreChange);
		ResourcesStore.removeChangeListener(resourcesStoreID, this.__handleStoreChange);
		AppsStore.removeChangeListener(appsStoreID, this.__handleStoreChange);
	},

	__handleStoreChange: function () {
		this.setState(this.__getState(this.props));
	}
});

export default AppDeleteResource;
