import sqlite3
import time
from server import wialon # Reutiliza sua função de conexão

def sync_data():
    conn = sqlite3.connect('tracking.db')
    cursor = conn.cursor()

    # 1. Sincronizar Unidades
    print("Sincronizando unidades...")
    res = wialon("core/search_items", {
        "spec": {"itemsType": "avl_unit", "propName": "sys_name", "propValueMask": "*", "sortType": "sys_name"},
        "force": 1, "flags": 1, "from": 0, "to": 0
    })
    
    for item in res.get("items", []):
        cursor.execute("INSERT OR REPLACE INTO units (id, name) VALUES (?, ?)", (item['id'], item['nm']))
        
        # 2. Buscar últimas mensagens (desde o último sync)
        # Aqui você usaria o messages/load_interval para pegar apenas o que é novo
        # e salvar na tabela 'positions'
    
    conn.commit()
    conn.close()

if __name__ == "__main__":
    while True:
        try:
            sync_data()
        except Exception as e:
            print(f"Erro no sync: {e}")
        time.sleep(60) # Espera 1 minuto