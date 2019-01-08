<template>
  <div>
    <Row>
      <Card>
        <div class="step-header-con">
          <h3>{{ stepData.title }}</h3>
          <h5>{{ stepData.describe }}</h5>
          <h4 style="margin-top: 5%">{{stepData.content}}</h4>
          <br>
          <br>
          <Button @click="back">返回</Button>
          <Button @click="del">撤销</Button>
        </div>
        <div class="step-content" style="height: 150px">
        </div>
        <Steps :current="1" style="margin-left: 10%">
          <Step v-for="item in stepList1" :title="item.title" :content="item.describe" :key="item.title"></Step>
        </Steps>
      </Card>
    </Row>
  </div>
</template>

<script>
  //
  import axios from 'axios'

  export default {
    name: 'put_ready',
    props: ['msg'],
    data () {
      return {
        stepData: {
          title: 'Yearning SQL查询系统',
          describe: `欢迎你！ ${sessionStorage.getItem('user')}`,
          content: '你的工单已提交审核，请等待管理员批准！'
        },
        step: {
          remark: '',
          timer: ''
        },
        stepList1: [
          {
            title: '提交',
            describe: '提交查询申请'
          },
          {
            title: '审核',
            describe: '等待审核结果'
          },
          {
            title: '查询',
            describe: '审核完毕，进入查询页面'
          }
        ]
      }
    },
    methods: {
      back () {
        this.$router.push({
          name: 'home_index'
        })
      },
      del () {
        axios.delete(`${this.$config.url}/query_worklf`)
          .then(() => {
            this.$router.push({
              name: 'serach-sql'
            })
          })
      }
    },
    mounted () {
      axios.put(`${this.$config.url}/query_worklf`, {'mode': 'status'})
        .then(res => {
          if (res.data === 1) {
            this.$router.push({
              name: 'querypage'
            })
          }
        })
    }
  }
</script>

<style lang="less">
  .step {
    &-header-con {
      text-align: center;
      h3 {
        margin: 10px 0;
      }
      h5 {
        margin: 0 0 5px;
      }
    }
    &-content {
      padding: 5px 20px 26px;
      margin-bottom: 20px;
      border-bottom: 1px solid #dbdddf;
    }
    &-form {
      padding-bottom: 10px;
      border-bottom: 1px solid #dbdddf;
      margin-bottom: 20px;
    }
  }
</style>
