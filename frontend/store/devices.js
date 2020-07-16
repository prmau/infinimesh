export const state = () => ({
  namespace: "",
  namespaces: [],
  pool: []
});

export const mutations = {
  namespace(state, val) {
    state.namespace = val;
  },
  namespaces(state, val) {
    state.namespaces = val;
  },
  pool(state, val) {
    state.pool = val;
  }
};

export const actions = {
  async get({ commit, state, rootState }) {
    let ns = "";
    if (state.namespaces.length) {
      ns = state.namespaces.filter(el => el.id == state.namespace)[0].name;
    } else {
      ns = rootState.auth.user.default_namespace.name;
    }

    const devices = await this.$axios.$get("/devices", {
      params: {
        namespace: ns
      }
    });
    commit("pool", devices.devices);
  },
  async getNamespaces({ commit }) {
    const namespaces = await this.$axios.$get("/namespaces");
    commit("namespaces", namespaces.namespaces);
  },
  setNamespace({ commit, dispatch }, ns) {
    commit("namespace", ns);
    dispatch("get");
  }
};
