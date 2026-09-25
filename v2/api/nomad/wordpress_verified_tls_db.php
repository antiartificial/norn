<?php
/**
 * Norn's WordPress MySQL verify-full db.php drop-in for WordPress 6.8.2.
 *
 * WordPress loads this after class-wpdb.php and before it creates $wpdb.
 * The allocation startup wrapper installs it as wp-content/db.php. The CA
 * path is an allocation-private Nomad template; credentials remain the
 * ordinary WORDPRESS_DB_* values consumed by wp-config-docker.php.
 */
class Norn_Verified_TLS_WPDB extends wpdb {
	public function db_connect( $allow_bail = true ) {
		$this->is_mysql = true;
		// MYSQL_SSL_CA is the adapter's intended declaration. SSL_CERT_FILE is
		// accepted for the existing stock-WordPress qualification shape, whose
		// private Nomad file is the same CA material.
		$ca = getenv( 'MYSQL_SSL_CA' );
		if ( ! is_string( $ca ) || $ca === '' ) {
			$ca = getenv( 'SSL_CERT_FILE' );
		}
		if ( ! is_string( $ca ) || $ca === '' || ! is_readable( $ca ) ) {
			$this->dbh = null;
			return $this->norn_bail_connect( $allow_bail );
		}

		mysqli_report( MYSQLI_REPORT_OFF );
		$this->dbh = mysqli_init();
		mysqli_options( $this->dbh, MYSQLI_OPT_SSL_VERIFY_SERVER_CERT, true );
		mysqli_ssl_set( $this->dbh, null, null, $ca, null, null );

		$host = $this->dbhost;
		$port = null;
		$socket = null;
		$is_ipv6 = false;
		$host_data = $this->parse_db_host( $this->dbhost );
		if ( $host_data ) {
			list( $host, $port, $socket, $is_ipv6 ) = $host_data;
		}
		if ( $is_ipv6 && extension_loaded( 'mysqlnd' ) ) {
			$host = "[$host]";
		}

		$connected = WP_DEBUG
			? mysqli_real_connect( $this->dbh, $host, $this->dbuser, $this->dbpassword, null, $port, $socket, MYSQLI_CLIENT_SSL )
			: @mysqli_real_connect( $this->dbh, $host, $this->dbuser, $this->dbpassword, null, $port, $socket, MYSQLI_CLIENT_SSL );
		if ( ! $connected || $this->dbh->connect_errno ) {
			$this->dbh = null;
			return $this->norn_bail_connect( $allow_bail );
		}

		if ( ! $this->has_connected ) {
			$this->init_charset();
		}
		$this->has_connected = true;
		$this->set_charset( $this->dbh );
		$this->ready = true;
		$this->set_sql_mode();
		$this->select( $this->dbname, $this->dbh );
		return true;
	}

	private function norn_bail_connect( $allow_bail ) {
		if ( ! $allow_bail ) {
			return false;
		}
		wp_load_translations_early();
		$message = '<h1>' . __( 'Error establishing a database connection' ) . "</h1>\n";
		$this->bail( $message, 'db_connect_fail' );
		return false;
	}
}

$wpdb = new Norn_Verified_TLS_WPDB( DB_USER, DB_PASSWORD, DB_NAME, DB_HOST );
