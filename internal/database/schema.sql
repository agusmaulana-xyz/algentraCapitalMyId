-- Embedded final SQLite schema. Initialized only for an empty database.

CREATE TABLE IF NOT EXISTS auth_locks (
    lock_key TEXT PRIMARY KEY
);

CREATE TABLE IF NOT EXISTS admin_users (
    username VARCHAR(128) NOT NULL, 
    password_hash VARCHAR(128) NOT NULL, 
    created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP, 
    PRIMARY KEY (username)
);

CREATE TABLE IF NOT EXISTS auth_sessions (
    session_hash VARCHAR(64) NOT NULL, 
    user_type VARCHAR(16) NOT NULL, 
    user_id VARCHAR(128) NOT NULL, 
    session_data TEXT NOT NULL, 
    created_at TEXT NOT NULL, 
    last_seen_at TEXT NOT NULL, 
    expires_at TEXT NOT NULL, 
    revoked_at TEXT, 
    PRIMARY KEY (session_hash)
);

CREATE INDEX IF NOT EXISTS ix_auth_sessions_expiry ON auth_sessions (expires_at);

CREATE INDEX IF NOT EXISTS ix_auth_sessions_user ON auth_sessions (user_type, user_id, revoked_at);

CREATE TABLE IF NOT EXISTS client_users (
    id INTEGER PRIMARY KEY AUTOINCREMENT, 
    email VARCHAR(320) NOT NULL, 
    password_hash VARCHAR(128) NOT NULL, 
    verified_at TEXT NOT NULL, 
    created_at TEXT NOT NULL
);

CREATE UNIQUE INDEX IF NOT EXISTS ux_client_users_email ON client_users (email);

CREATE TABLE IF NOT EXISTS ea_status (
    id INTEGER PRIMARY KEY AUTOINCREMENT, 
    active INTEGER NOT NULL, 
    last_heartbeat TEXT, 
    terminal VARCHAR(128), 
    version VARCHAR(64), 
    symbol VARCHAR(64)
);

CREATE TABLE IF NOT EXISTS email_verifications (
    email VARCHAR(320) NOT NULL, 
    code_hash VARCHAR(64) NOT NULL, 
    password_hash VARCHAR(128) NOT NULL, 
    expires_at TEXT NOT NULL, 
    resend_after TEXT NOT NULL, 
    attempts INTEGER NOT NULL, 
    send_count INTEGER NOT NULL, 
    send_window_started_at TEXT, 
    locked_until TEXT, 
    created_at TEXT NOT NULL, 
    PRIMARY KEY (email)
);

CREATE TABLE IF NOT EXISTS groups (
    chat_id VARCHAR(128) NOT NULL, 
    name VARCHAR(255) NOT NULL, 
    alias VARCHAR(255), 
    enabled INTEGER NOT NULL, 
    PRIMARY KEY (chat_id)
);

CREATE TABLE IF NOT EXISTS logs (
    id INTEGER PRIMARY KEY AUTOINCREMENT, 
    level VARCHAR(16) NOT NULL, 
    source VARCHAR(64) NOT NULL, 
    message TEXT NOT NULL, 
    created_at TEXT NOT NULL
);

CREATE INDEX IF NOT EXISTS ix_logs_created_at ON logs (created_at);

CREATE TABLE IF NOT EXISTS mt5_master_account_state (
    id INTEGER PRIMARY KEY AUTOINCREMENT, 
    balance FLOAT NOT NULL, 
    equity FLOAT NOT NULL, 
    floating_profit FLOAT NOT NULL, 
    currency VARCHAR(16) NOT NULL, 
    trade_mode VARCHAR(16) NOT NULL, 
    observed_at TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS mt5_master_copy_positions (
    id INTEGER PRIMARY KEY AUTOINCREMENT, 
    source_ticket VARCHAR(64) NOT NULL, 
    symbol VARCHAR(64) NOT NULL, 
    action VARCHAR(8) NOT NULL, 
    lots FLOAT NOT NULL, 
    entry_price FLOAT NOT NULL, 
    sl FLOAT, 
    tp FLOAT, 
    is_open INTEGER NOT NULL, 
    updated_at TEXT NOT NULL, 
    CONSTRAINT ux_mt5_master_copy_source_ticket UNIQUE (source_ticket)
);

CREATE INDEX IF NOT EXISTS ix_mt5_master_copy_open ON mt5_master_copy_positions (is_open);

CREATE TABLE IF NOT EXISTS mt5_master_copy_state (
    id INTEGER PRIMARY KEY AUTOINCREMENT, 
    last_snapshot_at TEXT
);

CREATE TABLE IF NOT EXISTS mt5_master_equity_candles (
    id INTEGER PRIMARY KEY AUTOINCREMENT, 
    account_key VARCHAR(192) NOT NULL, 
    minute_start TEXT NOT NULL, 
    open_equity FLOAT NOT NULL, 
    high_equity FLOAT NOT NULL, 
    low_equity FLOAT NOT NULL, 
    close_equity FLOAT NOT NULL, 
    currency VARCHAR(16) NOT NULL, 
    observed_at TEXT NOT NULL, 
    CONSTRAINT ux_mt5_master_equity_candle_account_minute UNIQUE (account_key, minute_start)
);

CREATE INDEX IF NOT EXISTS ix_mt5_master_equity_candle_account_minute ON mt5_master_equity_candles (account_key, minute_start);

CREATE TABLE IF NOT EXISTS mt5_master_equity_minute_samples (
    id INTEGER PRIMARY KEY AUTOINCREMENT, 
    sample_minute TEXT NOT NULL, 
    equity FLOAT NOT NULL, 
    currency VARCHAR(16) NOT NULL, 
    observed_at TEXT NOT NULL, 
    CONSTRAINT ux_mt5_master_equity_minute UNIQUE (sample_minute)
);

CREATE INDEX IF NOT EXISTS ix_mt5_master_equity_minute ON mt5_master_equity_minute_samples (sample_minute);

CREATE TABLE IF NOT EXISTS mt5_master_equity_samples (
    id INTEGER PRIMARY KEY AUTOINCREMENT, 
    sample_hour TEXT NOT NULL, 
    equity FLOAT NOT NULL, 
    currency VARCHAR(16) NOT NULL, 
    observed_at TEXT NOT NULL, 
    CONSTRAINT ux_mt5_master_equity_hour UNIQUE (sample_hour)
);

CREATE INDEX IF NOT EXISTS ix_mt5_master_equity_hour ON mt5_master_equity_samples (sample_hour);

CREATE TABLE IF NOT EXISTS mt5_master_market_state (
    id INTEGER PRIMARY KEY AUTOINCREMENT, 
    quotes_json TEXT NOT NULL, 
    observed_at TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS password_reset_codes (
    email_hash VARCHAR(64) NOT NULL, 
    code_hash VARCHAR(64) NOT NULL, 
    expires_at TEXT NOT NULL, 
    resend_after TEXT NOT NULL, 
    attempts INTEGER NOT NULL, 
    send_count INTEGER NOT NULL, 
    send_window_started_at TEXT, 
    locked_until TEXT, 
    created_at TEXT NOT NULL, 
    last_activity_at TEXT NOT NULL, 
    PRIMARY KEY (email_hash)
);

CREATE INDEX IF NOT EXISTS ix_password_reset_last_activity ON password_reset_codes (last_activity_at);

CREATE TABLE IF NOT EXISTS settings (
    key VARCHAR(128) NOT NULL, 
    value TEXT NOT NULL, 
    PRIMARY KEY (key)
);

CREATE TABLE IF NOT EXISTS signals (
    id INTEGER PRIMARY KEY AUTOINCREMENT, 
    group_id VARCHAR(128), 
    group_name VARCHAR(255), 
    message_id VARCHAR(128), 
    sender_id VARCHAR(128), 
    sender_name VARCHAR(255), 
    raw_text TEXT NOT NULL, 
    parsed_json TEXT, 
    normalized_text TEXT, 
    confidence FLOAT, 
    content_hash VARCHAR(64), 
    claimed_at TEXT, 
    status VARCHAR(32) NOT NULL, 
    created_at TEXT NOT NULL
);

CREATE INDEX IF NOT EXISTS ix_signals_created_at ON signals (created_at);

CREATE INDEX IF NOT EXISTS ix_signals_status_created_at ON signals (status, created_at);

CREATE UNIQUE INDEX IF NOT EXISTS ux_signals_content_hash ON signals (content_hash);

CREATE TABLE IF NOT EXISTS cs_conversations (
    id INTEGER PRIMARY KEY AUTOINCREMENT, 
    client_user_id INTEGER, 
    public_visitor_hash VARCHAR(64), 
    started_at TEXT NOT NULL, 
    expires_at TEXT NOT NULL, 
    CONSTRAINT ck_cs_conversation_identity CHECK ((client_user_id IS NOT NULL AND public_visitor_hash IS NULL) OR (client_user_id IS NULL AND public_visitor_hash IS NOT NULL)), 
    FOREIGN KEY(client_user_id) REFERENCES client_users (id) ON DELETE CASCADE
);

CREATE INDEX IF NOT EXISTS ix_cs_conversations_client_expiry ON cs_conversations (client_user_id, expires_at);

CREATE INDEX IF NOT EXISTS ix_cs_conversations_visitor_expiry ON cs_conversations (public_visitor_hash, expires_at);

CREATE TABLE IF NOT EXISTS ea_executions (
    id INTEGER PRIMARY KEY AUTOINCREMENT, 
    signal_id INTEGER NOT NULL, 
    leg INTEGER NOT NULL, 
    status VARCHAR(16) NOT NULL, 
    ticket VARCHAR(128), 
    lots FLOAT, 
    exec_price FLOAT, 
    reason TEXT, 
    created_at TEXT NOT NULL, 
    FOREIGN KEY(signal_id) REFERENCES signals (id), 
    CONSTRAINT ux_ea_executions_signal_leg UNIQUE (signal_id, leg)
);

CREATE TABLE IF NOT EXISTS mt5_accounts (
    id INTEGER PRIMARY KEY AUTOINCREMENT, 
    owner_id INTEGER NOT NULL, 
    label VARCHAR(80) NOT NULL, 
    server VARCHAR(128) NOT NULL, 
    login VARCHAR(32) NOT NULL, 
    plan VARCHAR(16) NOT NULL, 
    role VARCHAR(16) NOT NULL, 
    execution_mode VARCHAR(16) NOT NULL, 
    token_hash VARCHAR(64) NOT NULL, 
    active INTEGER NOT NULL, 
    last_seen_at TEXT, 
    created_at TEXT NOT NULL,
    rental_expires_at TEXT,
    ea_mode VARCHAR(16) NOT NULL DEFAULT 'SERVER',
    custom_ea_name VARCHAR(128),
    custom_ea_data BLOB,
    custom_ea_updated_at TEXT,
    custom_ea_inputs TEXT,
    custom_ea_params TEXT,
    custom_ea_paused INTEGER NOT NULL DEFAULT 0,
    FOREIGN KEY(owner_id) REFERENCES client_users (id) ON DELETE CASCADE,
    CONSTRAINT ux_mt5_owner_server_login UNIQUE (owner_id, server, login)
);

CREATE INDEX IF NOT EXISTS ix_mt5_accounts_owner_role ON mt5_accounts (owner_id, role);

CREATE UNIQUE INDEX IF NOT EXISTS ux_mt5_accounts_token_hash ON mt5_accounts (token_hash);

CREATE TABLE IF NOT EXISTS trades (
    id INTEGER PRIMARY KEY AUTOINCREMENT, 
    signal_id INTEGER, 
    ticket VARCHAR(128), 
    symbol VARCHAR(64), 
    action VARCHAR(16), 
    lots FLOAT, 
    entry FLOAT, 
    sl FLOAT, 
    tp TEXT, 
    exec_price FLOAT, 
    profit FLOAT NOT NULL, 
    result VARCHAR(16), 
    opened_at TEXT, 
    closed_at TEXT, 
    FOREIGN KEY(signal_id) REFERENCES signals (id)
);

CREATE UNIQUE INDEX IF NOT EXISTS ux_trades_ticket ON trades (ticket);

CREATE TABLE IF NOT EXISTS cs_chat_messages (
    id INTEGER PRIMARY KEY AUTOINCREMENT, 
    conversation_id INTEGER NOT NULL, 
    role VARCHAR(16) NOT NULL, 
    content TEXT NOT NULL, 
    created_at TEXT NOT NULL, 
    FOREIGN KEY(conversation_id) REFERENCES cs_conversations (id) ON DELETE CASCADE
);

CREATE INDEX IF NOT EXISTS ix_cs_chat_messages_conversation_created ON cs_chat_messages (conversation_id, created_at);

CREATE TABLE IF NOT EXISTS managed_copy_profiles (
    account_id INTEGER NOT NULL, 
    password_ciphertext TEXT NOT NULL, 
    follower_symbol VARCHAR(64) NOT NULL, 
    mode_lot VARCHAR(16) NOT NULL, 
    ratio_lot FLOAT NOT NULL, 
    lot_tetap FLOAT NOT NULL, 
    max_lot_per_order FLOAT NOT NULL, 
    max_lot_total FLOAT NOT NULL, 
    max_open_positions INTEGER NOT NULL, 
    login_status VARCHAR(24) NOT NULL, 
    login_message VARCHAR(240), 
    active INTEGER NOT NULL, 
    status VARCHAR(24) NOT NULL, 
    status_message VARCHAR(240), 
    last_seen_at TEXT, 
    updated_at TEXT NOT NULL, 
    PRIMARY KEY (account_id), 
    FOREIGN KEY(account_id) REFERENCES mt5_accounts (id) ON DELETE CASCADE
);

CREATE TABLE IF NOT EXISTS mt5_account_state (
    account_id INTEGER NOT NULL, 
    balance FLOAT NOT NULL, 
    equity FLOAT NOT NULL, 
    floating_profit FLOAT NOT NULL, 
    margin FLOAT NOT NULL, 
    free_margin FLOAT NOT NULL, 
    currency VARCHAR(16) NOT NULL, 
    trade_mode VARCHAR(16) NOT NULL, 
    allow_live_trading INTEGER NOT NULL, 
    terminal_trade_allowed INTEGER NOT NULL, 
    expert_trade_allowed INTEGER NOT NULL, 
    open_position_ids_json TEXT NOT NULL, 
    history_cursor VARCHAR(64) NOT NULL, 
    history_cursor_msc BIGINT NOT NULL, 
    observed_at TEXT NOT NULL, 
    PRIMARY KEY (account_id), 
    FOREIGN KEY(account_id) REFERENCES mt5_accounts (id) ON DELETE CASCADE
);

CREATE TABLE IF NOT EXISTS mt5_history_deals (
    id INTEGER PRIMARY KEY AUTOINCREMENT, 
    account_id INTEGER NOT NULL, 
    deal_ticket VARCHAR(64) NOT NULL, 
    position_id VARCHAR(64) NOT NULL, 
    deal_time TEXT NOT NULL, 
    deal_time_msc BIGINT NOT NULL, 
    symbol VARCHAR(64) NOT NULL, 
    action VARCHAR(8) NOT NULL, 
    entry VARCHAR(12) NOT NULL, 
    volume FLOAT NOT NULL, 
    price FLOAT NOT NULL, 
    profit FLOAT NOT NULL, 
    commission FLOAT NOT NULL, 
    swap FLOAT NOT NULL, 
    fee FLOAT NOT NULL, 
    FOREIGN KEY(account_id) REFERENCES mt5_accounts (id) ON DELETE CASCADE, 
    CONSTRAINT ux_mt5_history_account_ticket UNIQUE (account_id, deal_ticket)
);

CREATE INDEX IF NOT EXISTS ix_mt5_history_account_position ON mt5_history_deals (account_id, position_id);

CREATE INDEX IF NOT EXISTS ix_mt5_history_account_time ON mt5_history_deals (account_id, deal_time_msc);

CREATE TABLE IF NOT EXISTS payment_orders (
    id INTEGER PRIMARY KEY AUTOINCREMENT, 
    owner_id INTEGER NOT NULL, 
    email VARCHAR(320) NOT NULL, 
    label VARCHAR(80) NOT NULL, 
    server VARCHAR(128) NOT NULL, 
    login VARCHAR(32) NOT NULL, 
    plan VARCHAR(16) NOT NULL, 
    execution_mode VARCHAR(16) NOT NULL, 
    total_idr INTEGER NOT NULL, 
    rental_days INTEGER NOT NULL DEFAULT 10,
    payment_method VARCHAR(16) NOT NULL DEFAULT 'MANUAL',
    midtrans_order_id VARCHAR(64),
    midtrans_transaction_id VARCHAR(64),
    midtrans_status VARCHAR(32),
    qris_total_idr INTEGER NOT NULL DEFAULT 0,
    status VARCHAR(24) NOT NULL DEFAULT 'PENDING_PAYMENT',
    proof_filename VARCHAR(128), 
    account_id INTEGER, 
    admin_notified_at TEXT, 
    created_at TEXT NOT NULL, 
    updated_at TEXT NOT NULL, 
    FOREIGN KEY(account_id) REFERENCES mt5_accounts (id) ON DELETE SET NULL, 
    FOREIGN KEY(owner_id) REFERENCES client_users (id) ON DELETE CASCADE, 
    CONSTRAINT ux_payment_orders_account UNIQUE (account_id)
);

CREATE INDEX IF NOT EXISTS ix_payment_orders_owner_created ON payment_orders (owner_id, created_at);

CREATE INDEX IF NOT EXISTS ix_payment_orders_status_created ON payment_orders (status, created_at);

CREATE UNIQUE INDEX IF NOT EXISTS ux_payment_orders_midtrans_order
    ON payment_orders (midtrans_order_id)
    WHERE midtrans_order_id IS NOT NULL;

CREATE TABLE IF NOT EXISTS mt5_account_daily_profit (
    account_id INTEGER NOT NULL REFERENCES mt5_accounts(id) ON DELETE CASCADE,
    trade_date TEXT NOT NULL,
    currency VARCHAR(16) NOT NULL,
    profit REAL NOT NULL DEFAULT 0,
    updated_at TEXT NOT NULL,
    PRIMARY KEY (account_id, trade_date)
);

CREATE TABLE IF NOT EXISTS mt5_master_daily_profit (
    trade_date TEXT PRIMARY KEY,
    profit REAL NOT NULL DEFAULT 0,
    updated_at TEXT NOT NULL
);

CREATE INDEX IF NOT EXISTS ix_mt5_account_daily_profit_date ON mt5_account_daily_profit(account_id, trade_date DESC);
