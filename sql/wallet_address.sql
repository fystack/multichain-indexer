-- Remove enum constraints (if old schema existed)
DO $$
BEGIN
    IF EXISTS (
        SELECT 1
        FROM information_schema.columns
        WHERE table_name = 'wallet_addresses'
          AND column_name = 'type'
          AND udt_name = 'address_type'
    ) THEN
        ALTER TABLE wallet_addresses
            ALTER COLUMN type TYPE VARCHAR(64) USING type::text;
    END IF;

    IF EXISTS (
        SELECT 1
        FROM information_schema.columns
        WHERE table_name = 'wallet_addresses'
          AND column_name = 'standard'
          AND udt_name = 'address_standard'
    ) THEN
        ALTER TABLE wallet_addresses
            ALTER COLUMN standard TYPE VARCHAR(64) USING standard::text;
    END IF;
END $$;

DROP TYPE IF EXISTS address_type;
DROP TYPE IF EXISTS address_standard;

-- Create wallet_address table without enum constraints
CREATE TABLE IF NOT EXISTS wallet_addresses (
    id BIGSERIAL PRIMARY KEY,
    created_at TIMESTAMP WITH TIME ZONE DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMP WITH TIME ZONE DEFAULT CURRENT_TIMESTAMP,
    deleted_at TIMESTAMP WITH TIME ZONE,
    address VARCHAR(255) NOT NULL,
    type VARCHAR(64) NOT NULL,
    standard VARCHAR(64)
);

-- Create unique index on address
CREATE UNIQUE INDEX IF NOT EXISTS idx_unique_address ON wallet_addresses (address);

-- Create indexes for better query performance
CREATE INDEX IF NOT EXISTS idx_wallet_address_type ON wallet_addresses (type);
CREATE INDEX IF NOT EXISTS idx_wallet_address_standard ON wallet_addresses (standard);
CREATE INDEX IF NOT EXISTS idx_wallet_address_created_at ON wallet_addresses (created_at);

-- Add comments for documentation
COMMENT ON TABLE wallet_addresses IS 'Stores wallet addresses for different blockchain networks';
COMMENT ON COLUMN wallet_addresses.address IS 'The wallet address string';
COMMENT ON COLUMN wallet_addresses.type IS 'The blockchain network type (free text, no enum constraint)';
COMMENT ON COLUMN wallet_addresses.standard IS 'The token standard (free text, no enum constraint)';

-- Insert sample data
INSERT INTO wallet_addresses (address, type, standard) VALUES
('TAWdqnuYCNU3dKsi7pR8d7sDkx1Evb2giV', 'tron', 'trc20'),
('TT1j2adMBb6bF2K8C2LX1QkkmSXHjiaAfw', 'tron', 'trc20'),
('Ef8zMzMzMzMzMzMzMzMzMzMzMzMzMzMzMzMzMzMzMzMzM0vF', 'ton', 'native'),
('EQDKHZ7e70CzqdvZCC83Z4WVR8POC_ZB0J1Y4zo88G-zCXmC', 'ton', 'native'),
('EQBeab7D38RIwypegbN7YZgQzwDbb8QfMMwY8ouJc3qPl91M', 'ton', 'native')
ON CONFLICT (address) DO NOTHING;
