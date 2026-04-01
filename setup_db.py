import sqlite3

def create_database():
    # Conecta (ou cria) o arquivo do banco
    conn = sqlite3.connect('tracking.db')
    cursor = conn.cursor()

    print("Criando tabelas no tracking.db...")

    # 1. Tabela de Clientes (Organizações)
    cursor.execute('''
        CREATE TABLE IF NOT EXISTS clients (
            id INTEGER PRIMARY KEY AUTOINCREMENT,
            wialon_id INTEGER UNIQUE,
            name TEXT NOT NULL,
            document TEXT, -- CPF/CNPJ
            contact TEXT
        )
    ''')

    # 2. Tabela de Grupos (Associados a Clientes)
    cursor.execute('''
        CREATE TABLE IF NOT EXISTS groups (
            id INTEGER PRIMARY KEY AUTOINCREMENT,
            wialon_id INTEGER UNIQUE,
            name TEXT NOT NULL,
            client_id INTEGER,
            FOREIGN KEY (client_id) REFERENCES clients(id)
        )
    ''')

    # 3. Tabela de Veículos (Units)
    # Inclui colunas para a list.html e relação com grupos/clientes
    cursor.execute('''
        CREATE TABLE IF NOT EXISTS units (
            id INTEGER PRIMARY KEY, -- Usaremos o ID da Wialon como chave primária
            name TEXT NOT NULL,
            plate TEXT,             -- Placa
            model TEXT,             -- Modelo do veículo
            group_id INTEGER,
            client_id INTEGER,
            last_update INTEGER,    -- Timestamp da última mensagem recebida
            FOREIGN KEY (group_id) REFERENCES groups(id),
            FOREIGN KEY (client_id) REFERENCES clients(id)
        )
    ''')

    # 4. Tabela de Histórico e Eventos (Onde os dados da API serão salvos)
    cursor.execute('''
        CREATE TABLE IF NOT EXISTS positions (
            id INTEGER PRIMARY KEY AUTOINCREMENT,
            unit_id INTEGER,
            timestamp INTEGER,
            lat REAL,
            lon REAL,
            speed INTEGER,
            course INTEGER,
            ignition INTEGER,    -- Valor de p_user_d0
            event_code INTEGER,  -- Valor de p_user_d3 (101, 102, etc)
            address TEXT,
            FOREIGN KEY (unit_id) REFERENCES units(id)
        )
    ''')

    # 5. Criação de Índices para deixar as buscas de relatórios instantâneas
    cursor.execute('CREATE INDEX IF NOT EXISTS idx_pos_unit_time ON positions (unit_id, timestamp)')
    cursor.execute('CREATE INDEX IF NOT EXISTS idx_pos_event ON positions (event_code)')

    conn.commit()
    conn.close()
    print("Banco de dados 'tracking.db' criado com sucesso!")

if __name__ == "__main__":
    create_database()