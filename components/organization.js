// Função para salvar a empresa e filtrar
function updateSelectedCompany(companyId) {
    localStorage.setItem('selected_company_id', companyId);

    // Se existir uma função de filtro específica da página, chame-a aqui
    if (typeof filterByCompany === "function") {
        filterByCompany(companyId);
    }
}

// Função para aplicar a seleção salva ao carregar a página
function applySavedCompany() {
    const savedId = localStorage.getItem('selected_company_id');
    const select = document.getElementById('companySelect');

    if (savedId && select) {
        select.value = savedId;
        // Dispara o filtro inicial se necessário
        if (typeof filterByCompany === "function") {
            filterByCompany(savedId);
        }
    }
}

// Chame applySavedCompany dentro do seu DOMContentLoaded
document.addEventListener('DOMContentLoaded', () => {
    // ... suas lógicas existentes ...
    applySavedCompany();
});

// Dados centralizados para todo o ecossistema Track7
window.Track7Data = {
    groups: [
        { id: 1, name: "Transportadora Rápido", description: "Grupo principal", company: "Transportadora Rápido Ltda", configs: { tracking: true, reports: true, alerts: true }, equipmentCount: 45 },
        { id: 2, name: "Logística Express", description: "Configurações avançadas", company: "Logística Express S.A.", configs: { tracking: true, reports: false, alerts: true }, equipmentCount: 28 }
    ],
    clients: [
        { id: 1, tradeName: "Rápido Transportes", companyName: "Transportadora Rápido Ltda", status: "active" },
        { id: 2, tradeName: "Log Express", companyName: "Logística Express S.A.", status: "active" }
    ],
    equipments: [
        { id: 1, imei: "860123456789012", vehicle: "Caminhão Mercedes Actros", status: "active" }
    ]
};