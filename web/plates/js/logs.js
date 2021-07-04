angular.module('appControllers').controller('LogsCtrl', LogsCtrl);      // get the main module contollers set
LogsCtrl.$inject = ['$scope', '$state', '$http'];                                   // Inject my dependencies

// create our controller function with all necessary logic
function LogsCtrl($scope, $state, $http) {
	$scope.$parent.helppage = 'plates/logs-help.html';

	// just a couple environment variables that may bve useful for dev/debugging but otherwise not significant
	$scope.userAgent = navigator.userAgent;
    $scope.deviceViewport = 'screen = ' + window.screen.width + ' x ' + window.screen.height;

	$http.get(URL_GET_FLIGHTLOG_LIST).then(function(response) {
		let loglist = angular.fromJson(response.data);
		for (log of loglist) {
			let start = new Date(log.StartTs);
			let end = new Date(log.EndTs);
			log.StartDateReadable = start.toLocaleDateString();
			log.StartTimeReadable = start.toLocaleTimeString();
			log.EndDateReadable = end.toLocaleDateString();
			log.EndTimeReadable = end.toLocaleTimeString();
			log.IsEditing = (log.LogName == "");
			log.ExportFormat = "";
		}
		$scope.logList = loglist;
	});

	$scope.updateLogName = function(log) {
		let params = {
			id: log.LogId,
			name: log.LogName
		};
		$http.get(URL_FLIGHTLOG_RENAME, {params: params}).then(function() {}, function() {});
		log.IsEditing = (log.LogName == "");
		console.log(log);
	}

	$scope.startEditing = function(log) {
		log.IsEditing = true;
	}

	$scope.downloadLog = function(log) {
		if (log.ExportFormat == "")
			return;

		let opts = log.ExportFormat.split(";");
		let request = {
			id: log.LogId,
			format: opts[0],
			traffic: (opts.length == 2 && opts[1] == "traffic")
		};
		let dt = new Date(log.StartTs);
		if (log.LogName.length > 0) {
			request.filename = dt.toISOString().split("T")[0] + " " + log.LogName + "." + request.format
		} else {
			request.filename = dt.toISOString() + "." + request.format
		}

		// TODO: better way to do that with angular?
		log.ExportFormat = ""; // reset form field
		window.location = URL_FLIGHTLOG_DOWNLOAD + "?id=" + request.id + "&format=" + request.format + "&traffic=" + request.traffic + "&filename=" + encodeURIComponent(request.filename);
		/*$http.get(URL_FLIGHTLOG_DOWNLOAD, {params: request}).then(function (response) {
			log.ExportFormat = ""; // reset form field
		}, function (response) {
			log.ExportFormat = ""; // reset form field
		});*/
	}

	$scope.deleteLog = function(log) {
		$http.get(URL_FLIGHTLOG_DELETE, {params: {"id": log.LogId}}).then(
			function() {
				let idx = $scope.logList.indexOf(log);
				if (idx >= 0)
					$scope.logList.splice(idx, 1);
				$scope.apply();
			}
		)
	}
}
