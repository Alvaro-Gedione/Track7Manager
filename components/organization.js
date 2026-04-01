function updateSelectedCompany(companyId) {
    // 1. Salva a escolha no localStorage imediatamente
    localStorage.setItem('selected_company_id', companyId);

    // 2. Dispara um evento customizado para que as páginas abertas reajam à mudança
    document.dispatchEvent(new CustomEvent('organizationChanged', {
        detail: { companyId: companyId }
    }));

    // 3. Se existir uma função de filtro local na página, ela é chamada
    if (typeof filterByCompany === "function") {
        filterByCompany(companyId);
    }
}

function applySavedCompany() {
    // Recupera o ID salvo
    const savedId = localStorage.getItem('selected_company_id');
    const select = document.getElementById('companySelect') || document.getElementById('orgSelector');

    if (savedId && select) {
        select.value = savedId;
        // Aplica o filtro inicial se a função existir
        if (typeof filterByCompany === "function") {
            filterByCompany(savedId);
        }
    }
}



function loadGlobalOrganizations() {
    const selects = [
        document.getElementById('companySelect'),
        document.getElementById('orgSelector')
    ];

    const organizations = [
        { id: "1", name: "Nova Suissa BH" },
        { id: "2", name: "Jacareí Transportes Urbano" }
    ];

    selects.forEach(select => {
        if (select) {
            // Limpa opções atuais e mantém apenas a padrão se necessário
            select.innerHTML = '<option value="">Todas as Empresas</option>';
            organizations.forEach(org => {
                const option = document.createElement('option');
                option.value = org.id;
                option.textContent = org.name;
                select.appendChild(option);
            });
        }
    });
}
// Adicione esta lógica ou similar ao seu organization.js existente
const EQUIPAMENTOS_POR_ORG = {
    "1": [ // Nova Suissa BH
        { id: 101, name: "Caminhão Betoneira - NS01", plate: "ABC-1234", totalEvents: 12, totalDistance: 450, trips: [] },
        { id: 102, name: "Escavadeira - NS02", plate: "DEF-5678", totalEvents: 3, totalDistance: 80, trips: [] }
    ],
    "2": [ // Jacareí Transportes Urbano
        { id: 201, name: "Ônibus Urbano - J01", plate: "GHI-9012", totalEvents: 45, totalDistance: 1200, trips: [] },
        { id: 202, name: "Ônibus Urbano - J02", plate: "JKL-3456", totalEvents: 28, totalDistance: 950, trips: [] }
    ]
};
// No topo do seu script
const EQUIPAMENTOS_PROTOTIPO = [
    { id: 1, groupId: 1, imei: "865513072687276", name: "31282 - VOLKS - 17.260 ( C )", plate: "31282", totalEvents: 0, totalDistance: 0, trips: [] },
    { id: 2, groupId: 2, imei: "865513072274851", name: "1583 - M.B. - LO 916 ( C )", plate: "1583", totalEvents: 0, totalDistance: 0, trips: [] }
];

function getEquipmentsBySelectedOrg() {
    const orgId = localStorage.getItem('selected_company_id') || "1";
    return EQUIPAMENTOS_POR_ORG[orgId] || [];
}

document.addEventListener('DOMContentLoaded', () => {
    loadGlobalOrganizations();
    applySavedCompany();
});