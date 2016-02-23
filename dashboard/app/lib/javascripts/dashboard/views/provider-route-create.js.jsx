import Config from 'dashboard/config';
import Dispatcher from 'dashboard/dispatcher';
import ProvidersStore from 'dashboard/stores/providers';
import AppRoutesStore from 'dashboard/stores/app-routes';
import Modal from 'Modal';

var providersStoreID = 'default';
var appRoutesStoreID = function (state) {
	return {
		appId: state.providerApp.id
	};
};

var CreateProviderRoute = React.createClass({
	displayName: "Views.CreateProviderRoute",

	render: function () {
		var provider = this.state.provider;
		if (provider === null) {
			return <div />;
		}

		var attrs = Config.PROVIDER_ATTRS[provider.name];

		return (
			<Modal onShow={function(){}} onHide={this.props.onHide} visible={this.state.hasExternalRoute === false}>
				<header>
					<h1>Create external route for {attrs.title}?</h1>
				</header>

				<p className="alert-warning">
					<strong>WARNING:</strong> This will expose {attrs.title} outside the cluster. You will be able to use the port number on the next screen to restrict access through your firewall.
				</p>

				<button className="btn-green btn-block" onClick={this.__handleSkipBtnClick}>No thanks, continue without route</button>

				<button className="btn-green btn-block" disabled={ this.state.isCreating } onClick={this.__handleCreateBtnClick}>{this.state.isCreating ? "Please wait..." : "I understand, create external route"}</button>
			</Modal>
		);
	},

	getInitialState: function () {
		return this.__getState(this.props);
	},

	componentDidMount: function () {
		ProvidersStore.addChangeListener(providersStoreID, this.__handleStoreChange);
		if (this.state.appRoutesStoreID) {
			this.__checkForRoute();
		}
	},

	componentWillUnmount: function () {
		ProvidersStore.removeChangeListener(providersStoreID, this.__handleStoreChange);
		if (this.state.appRoutesStoreID !== null) {
			AppRoutesStore.removeChangeListener(this.state.appRoutesStoreID, this.__handleStoreChange);
		}
	},

	__handleSkipBtnClick: function (e) {
		e.preventDefault();
		if (this.props.provisionResource) {
			// continue with provisioning
			this.__actionTaken = true;
			Dispatcher.dispatch({
				name: 'PROVISION_RESOURCE',
				providerID: this.props.providerID
			});
		} else {
			// go back to where we came from
			this.props.onHide();
		}
	},

	__handleCreateBtnClick: function (e) {
		e.preventDefault();
		var provider = this.state.provider;
		var app = this.state.providerApp;
		var attrs = Config.PROVIDER_ATTRS[provider.name];
		this.setState({
			isCreating: true
		});
		Dispatcher.dispatch({
			name: 'CREATE_EXTERNAL_PROVIDER_ROUTE',
			providerAppID: app.id,
			serviceName: attrs.serviceName
		});
	},

	__getState: function (props) {
		var prevState = this.state || {};
		var state = {
			appRoutesStoreID: prevState.appRoutesStoreID || null,
			hasExternalRoute: prevState.hasExternalRoute === undefined ? null : prevState.hasExternalRoute
		};

		var providersState = ProvidersStore.getState(providersStoreID);
		state.provider = providersState.fetched ? providersState.providers.find(function (provider) {
			return provider.id === props.providerID;
		}) : null;
		state.providerApp = state.provider ? providersState.providerApps[state.provider.id] : null;

		if (state.providerApp && state.appRoutesStoreID === null) {
			state.appRoutesStoreID = appRoutesStoreID(state);
			AppRoutesStore.addChangeListener(state.appRoutesStoreID, this.__handleStoreChange);
		}

		return state;
	},

	__checkForRoute: function () {
		if (this.__actionTaken) {
			return;
		}

		var appRoutesState = AppRoutesStore.getState(this.state.appRoutesStoreID);
		var routes = appRoutesState.fetched ? appRoutesState.routes : null;
		if (routes === null) {
			return;
		}
		var hasExternalRoute = !!routes.find(function (route) {
			return route.leader === true && route.type === 'tcp';
		});

		if (hasExternalRoute && this.props.provisionResource) {
			// continue with provisioning
			this.__actionTaken = true;
			Dispatcher.dispatch({
				name: 'PROVISION_RESOURCE',
				providerID: this.props.providerID
			});
		} else if (hasExternalRoute) {
			// go back to resource
			this.__actionTaken = true;
			this.props.onHide();
		}
		if ( !hasExternalRoute ) {
			this.setState({
				hasExternalRoute: false
			});
		}
	},

	__handleStoreChange: function () {
		this.setState(this.__getState(this.props));
		this.__checkForRoute();
	}
});

export default CreateProviderRoute;
