// topnav.js - Componente de navegação reutilizável

class TopNavigation extends HTMLElement {
    constructor() {
        super();
        this.theme = localStorage.getItem('theme') || 'dark';
    }

    getLogoPath() {
        const path = window.location.pathname;
        const filename = path.split('/').pop() || 'index.html';

        // Se estiver na página index.html (ou raiz), usa um caminho, senão usa outro
        if (filename === 'index.html' || filename === '' || path.endsWith('/')) {
            return '../public/img/logo_track7.png';
        } else {
            return '../../public/img/logo_track7.png';
        }
    }

    getIndexPath() {
        const path = window.location.pathname;
        const filename = path.split('/').pop() || 'index.html';

        // Se estiver na página index.html (ou raiz), usa um caminho, senão usa outro
        if (filename === 'index.html' || filename === '' || path.endsWith('/')) {
            return '../pages/index.html';
        } else {
            return '../index.html';
        }
    }

    getMenuPath() {
        const path = window.location.pathname;
        const filename = path.split('/').pop() || 'menu.html';

        // Se estiver na página index.html (ou raiz), usa um caminho, senão usa outro
        if (filename === 'menu.html') {
            return '../../pages/menu/menu.html';
        } if (filename === 'index.html') {
            return '../pages/menu/menu.html';
        } else {
            return '../menu/menu.html';
        }
    }

    connectedCallback() {
        this.render();
        this.attachEventListeners();
    }

    render() {
        // Cor Cinza Chumbo fixa para o header
        const headerBg = '#181818';
        const logoPath = this.getLogoPath();
        const indexPath = this.getIndexPath();
        const menuPath = this.getMenuPath();

        this.innerHTML = `
            <header style="background-color: ${headerBg};
                          padding: 0 40px;
                          height: 75px;
                          display: flex;
                          align-items: center;
                          justify-content: space-between;
                          position: sticky;
                          top: 0;
                          z-index: 1000;
                          box-shadow: 0 4px 20px rgba(0, 0, 0, 0.5);
                          transition: background-color 0.3s ease;">
                
                <div class="logo-area" style="display: flex; align-items: center; gap: 20px;">
                    <div style="display: flex; align-items: center;">
                        <img src="${logoPath}" alt="Track7" style="height: 100px; width: auto; object-fit: contain;">
                    </div>

                    <nav>
                        <ul style="list-style: none; display: flex; gap: 5px; margin: 0; padding: 0;">
                            <li class="nav-item" style="position: relative; padding: 12px 18px; cursor: pointer; border-radius: 6px; transition: all 0.4s cubic-bezier(0.175, 0.885, 0.32, 1.275); color: #9e9e9e; font-size: 13px; font-weight: 600; text-transform: uppercase; letter-spacing: 0.5px;">
                                Menu Geral <i class="fas fa-chevron-down" style="font-size: 10px; margin-left: 5px;"></i>
                                <div class="dropdown-content" style="position: absolute; top: 100%; left: 0; background: #1e1e1e; border-top: 3px solid #f264220a; border-radius: 0 0 12px 12px; min-width: 220px; box-shadow: 0 15px 35px rgba(0, 0, 0, 0.8); opacity: 0; visibility: hidden; transform: translateY(15px); transition: all 0.4s cubic-bezier(0.175, 0.885, 0.32, 1.275); padding: 10px 0;">
                                    <a href="${menuPath}" style="padding: 12px 20px; display: flex; align-items: center; gap: 12px; color: #a0a0a0; text-decoration: none; font-size: 13px; transition: 0.2s;"><i class="fas fa-tachometer-alt"></i> Menu Geral</a>
                                    <a href="#" style="padding: 12px 20px; display: flex; align-items: center; gap: 12px; color: #a0a0a0; text-decoration: none; font-size: 13px; transition: 0.2s;"><i class="fas fa-star"></i> Favoritos</a>
                                </div>
                            </li>
                        </ul>
                    </nav>
                </div>

                <div class="user-info">
                    <div class="user-badge" style="display: flex; align-items: center; gap: 12px; background: rgba(255, 255, 255, 0.03); padding: 8px 16px; border-radius: 30px; border: 1px solid rgba(255, 255, 255, 0.08);">
                        <i class="${this.theme === 'dark' ? 'fas fa-sun' : 'fas fa-moon'}" id="themeToggle" style="color: #9e9e9e; margin-right: 5px; cursor: pointer; font-size: 18px;"></i>
                        
                        <a href="${indexPath}" class="home-btn" style="text-decoration: none; color: #9e9e9e; font-size: 18px; transition: all 0.3s ease; display: flex; align-items: center; margin-right: 5px;">
                            <i class="fas fa-home"></i>
                        </a>

                        <i class="far fa-bell" style="color: #9e9e9e; margin-right: 10px; cursor: pointer;"></i>
                        <small style="color: #9e9e9e; font-weight: 600;">Alvaro</small>
                        <img src="https://i.pravatar.cc/100?u=alvaro" alt="User" style="width: 32px; height: 32px; border-radius: 50%; border: 2px solid #BE6004;">
                    </div>
                </div>
            </header>
        `;
        this.addHoverStyles();
    }

    addHoverStyles() {
        const style = document.createElement('style');
        style.textContent = `
            .nav-item:hover { color: #BE6004 !important; background: rgba(255, 255, 255, 0.03) !important; }
            .nav-item:hover .dropdown-content { opacity: 1 !important; visibility: visible !important; transform: translateY(0) !important; }
            .dropdown-content a:hover { background: rgba(242, 101, 34, 0.1) !important; color: #BE6004 !important; padding-left: 25px !important; }
            .home-btn:hover { color: #BE6004 !important; transform: scale(1.15); }
        `;
        this.appendChild(style);
    }

    attachEventListeners() {
        const themeToggle = this.querySelector('#themeToggle');
        if (themeToggle) {
            themeToggle.addEventListener('click', () => this.toggleTheme());
        }
    }

    toggleTheme() {
        this.theme = this.theme === 'dark' ? 'light' : 'dark';
        localStorage.setItem('theme', this.theme);

        document.dispatchEvent(new CustomEvent('themeChanged', {
            detail: { theme: this.theme }
        }));

        this.updateBodyClass();
        this.rerender(); // Re-renderiza para aplicar a troca do ícone Sol/Lua
    }

    updateBodyClass() {
        document.body.className = `theme-${this.theme}`;
    }

    rerender() {
        this.render();
        this.attachEventListeners();
    }
}

customElements.define('top-navigation', TopNavigation);