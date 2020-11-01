<template>
  <button @click="play" :aria-label="'Play VLC'" :title="'Play VLC'" id="play-button" class="action">
    <i class="material-icons">play_circle_outline</i>
    <span>Play VLC</span>
    <span v-if="selectedCount > 0" class="counter">{{ selectedCount }}</span>
  </button>
</template>

<script>
import {mapGetters, mapState} from 'vuex'
import { files as api } from '@/api'
export default {
  name: 'play-button',
  computed: {
    ...mapState(['req', 'selected']),
    ...mapGetters(['isListing', 'selectedCount'])
  },
  methods: {
    play: function () {
      console.log(api.fetch(this.req.items[this.selected[0]].url));
      let files = []
      for (let i of this.selected) {
        files.push(this.req.items[i].url)
      }
      api.download("m3u", ...files)      
    }
  }
}
</script>
